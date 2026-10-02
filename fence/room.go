// Package fence implements an in-memory fenced full-text search library
// for a single data room. All corpus, folder tree, grants and search
// state live in memory; callers invoke it directly and serially.
package fence

import (
	"errors"
	"fmt"
	"sort"
)

// Folder is one node of the room folder tree. ParentID of a root is -1.
type Folder struct {
	ID       int
	ParentID int
}

// Document is a single versioned text already extracted by the caller.
type Document struct {
	ID       int
	FolderID int
	Text     string
}

// Hit is one search result: a document ID and its relevance score.
type Hit struct {
	DocID int
	Score int64
}

const maxDepth = 10

// Room holds the immutable corpus, the folder tree and per-user grants.
type Room struct {
	loaded bool

	docIDs  []int    // internal doc index -> caller doc ID
	docOff  []uint32 // per doc: start offset into docTermIDs/docTfs, len nDocs+1
	docTerm []uint32 // per doc: sorted term IDs
	docTf   []uint32 // per doc: term frequencies, aligned with docTerm

	postOff []uint32 // per term: start offset into postDoc/postTf, len nTerms+1
	postDoc []uint32 // postings: internal doc index, ascending per term
	postTf  []uint32 // postings: term frequency, aligned with postDoc

	terms       map[string]uint32 // word -> term ID
	folderIdx   map[int]int32     // caller folder ID -> internal folder index
	subtreeDocs [][]int32         // per folder: sorted unique internal doc indexes in subtree

	grants map[int][]int32  // user ID -> granted internal folder indexes
	vis    map[int]*visible // user ID -> cached visible set
}

// visible is the cached visible document set of one user.
type visible struct {
	bitmap []uint64 // bit i set => internal doc i visible
	docs   []int32  // sorted visible internal doc indexes
}

// NewRoom returns an empty room ready for Load.
func NewRoom() *Room {
	return &Room{
		grants: map[int][]int32{},
		vis:    map[int]*visible{},
	}
}

// Load stores the room corpus and folder tree. It must be called once,
// before any ReplaceGrants or Search. Corpus and tree are immutable after.
func (r *Room) Load(folders []Folder, docs []Document) error {
	if r.loaded {
		return errors.New("fence: room already loaded")
	}
	folderIdx, children, err := buildTree(folders)
	if err != nil {
		return err
	}
	termID := map[string]uint32{}
	var postDocTmp [][]uint32
	var postTfTmp [][]uint32

	docIDs := make([]int, len(docs))
	seen := make(map[int]struct{}, len(docs))
	docOff := make([]uint32, len(docs)+1)
	var docTerm, docTf []uint32
	tf := map[uint32]uint32{}
	for i, d := range docs {
		if _, dup := seen[d.ID]; dup {
			return fmt.Errorf("fence: duplicate document ID %d", d.ID)
		}
		seen[d.ID] = struct{}{}
		if _, ok := folderIdx[d.FolderID]; !ok {
			return fmt.Errorf("fence: document %d in unknown folder %d", d.ID, d.FolderID)
		}
		docIDs[i] = d.ID
		for k := range tf {
			delete(tf, k)
		}
		for _, w := range tokenize(d.Text) {
			id, ok := termID[w]
			if !ok {
				id = uint32(len(postDocTmp))
				termID[w] = id
				postDocTmp = append(postDocTmp, nil)
				postTfTmp = append(postTfTmp, nil)
			}
			tf[id]++
		}
		ids := make([]uint32, 0, len(tf))
		for id := range tf {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		for _, id := range ids {
			docTerm = append(docTerm, id)
			docTf = append(docTf, tf[id])
			postDocTmp[id] = append(postDocTmp[id], uint32(i))
			postTfTmp[id] = append(postTfTmp[id], tf[id])
		}
		docOff[i+1] = uint32(len(docTerm))
	}

	nTerms := len(postDocTmp)
	postOff := make([]uint32, nTerms+1)
	for t := 0; t < nTerms; t++ {
		postOff[t+1] = postOff[t] + uint32(len(postDocTmp[t]))
	}
	postDoc := make([]uint32, 0, postOff[nTerms])
	postTf := make([]uint32, 0, postOff[nTerms])
	for t := 0; t < nTerms; t++ {
		postDoc = append(postDoc, postDocTmp[t]...)
		postTf = append(postTf, postTfTmp[t]...)
	}

	subtreeDocs, err := buildSubtrees(children, folderIdx, docs)
	if err != nil {
		return err
	}

	r.loaded = true
	r.docIDs = docIDs
	r.docOff = docOff
	r.docTerm = docTerm
	r.docTf = docTf
	r.postOff = postOff
	r.postDoc = postDoc
	r.postTf = postTf
	r.terms = termID
	r.folderIdx = folderIdx
	r.subtreeDocs = subtreeDocs
	r.grants = map[int][]int32{}
	r.vis = map[int]*visible{}
	return nil
}

// buildTree validates folders, enforces the depth limit and returns the
// folder index map plus children lists keyed by internal folder index.
func buildTree(folders []Folder) (map[int]int32, [][]int32, error) {
	idx := make(map[int]int32, len(folders))
	for i, f := range folders {
		if _, dup := idx[f.ID]; dup {
			return nil, nil, fmt.Errorf("fence: duplicate folder ID %d", f.ID)
		}
		idx[f.ID] = int32(i)
	}
	parent := make([]int32, len(folders))
	children := make([][]int32, len(folders))
	for i, f := range folders {
		if f.ParentID < 0 {
			parent[i] = -1
			continue
		}
		p, ok := idx[f.ParentID]
		if !ok {
			return nil, nil, fmt.Errorf("fence: folder %d has unknown parent %d", f.ID, f.ParentID)
		}
		parent[i] = int32(p)
		children[p] = append(children[p], int32(i))
	}
	depth := make([]int32, len(folders))
	state := make([]uint8, len(folders)) // 0 unvisited, 1 in stack, 2 done
	var depthOf func(i int32) (int32, error)
	depthOf = func(i int32) (int32, error) {
		if state[i] == 2 {
			return depth[i], nil
		}
		if state[i] == 1 {
			return 0, fmt.Errorf("fence: folder tree has a cycle at folder %d", folders[i].ID)
		}
		state[i] = 1
		d := int32(1)
		if parent[i] >= 0 {
			pd, err := depthOf(parent[i])
			if err != nil {
				return 0, err
			}
			d = pd + 1
		}
		if d > maxDepth {
			return 0, fmt.Errorf("fence: folder %d exceeds maximum depth %d", folders[i].ID, maxDepth)
		}
		state[i] = 2
		depth[i] = d
		return d, nil
	}
	for i := range folders {
		if _, err := depthOf(int32(i)); err != nil {
			return nil, nil, err
		}
	}
	return idx, children, nil
}

// buildSubtrees computes, per folder, the sorted unique internal indexes
// of all documents in its subtree.
func buildSubtrees(children [][]int32, folderIdx map[int]int32, docs []Document) ([][]int32, error) {
	own := make([][]int32, len(children))
	for i, d := range docs {
		fi := folderIdx[d.FolderID]
		own[fi] = append(own[fi], int32(i))
	}
	out := make([][]int32, len(children))
	var fill func(i int32) []int32
	fill = func(i int32) []int32 {
		merged := append([]int32(nil), own[i]...)
		for _, c := range children[i] {
			merged = append(merged, fill(c)...)
		}
		sort.Slice(merged, func(a, b int) bool { return merged[a] < merged[b] })
		out[i] = merged
		return merged
	}
	for i := range children {
		if out[i] == nil {
			fill(int32(i))
		}
	}
	return out, nil
}
