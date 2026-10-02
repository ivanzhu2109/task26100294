// Package fence 实现数据室围栏全文检索库。
// 正文、目录、授权与检索状态全部保存在内存中，由上层直接调用。
package fence

import (
	"errors"
	"fmt"
	"sort"
)

// maxTreeDepth 是目录树允许的最大层数（根为第 1 层）。
const maxTreeDepth = 10

// Folder 是目录树中的一个节点。根节点的 ParentID 为 0。
type Folder struct {
	ID       int
	ParentID int
}

// Document 是上层已抽取的当前版本正文。
type Document struct {
	ID       int
	FolderID int
	Text     string
}

// Hit 是一条命中结果。
type Hit struct {
	DocID int
	Score uint64
}

// Room 保存单个房间的语料、目录树、全部用户授权与检索状态。
// 装载后语料与目录不可变，所有调用串行执行，因此内部无锁。
type Room struct {
	folderIdx    map[int]int32 // 外部目录ID -> 内部序号
	folders      []*folderNode // 按内部序号
	folderOf     []int32       // 内部文档序号 -> 内部目录序号
	docIDs       []int         // 内部文档序号 -> 外部文档ID
	docTerms     [][]uint32    // 内部文档序号 -> 排序去重后的词ID
	docTFs       [][]uint32    // 与 docTerms 对齐的词频
	postings     [][]posting   // 词ID -> 倒排表（按内部文档序号升序）
	termIDs      map[string]uint32
	users        map[int]*userState
	scratchVis   []uint32 // 目录去重暂存（与 scratchEpoch 比较）
	scratchEpoch uint32
	searchEpoch  uint32   // 检索去重代际
	scEpoch      []uint32 // 文档计分暂存代际
	scScore      []uint64 // 文档得分暂存
	scCnt        []uint32 // 文档命中词数暂存
	scTouched    []uint32 // 本次检索触碰过的文档
	aDocs        []uint32 // 路径A枚举出的可见文档
	aTF          [][4]uint32
	aMask        []uint8
}

// NewRoom 创建一个空房间。
func NewRoom() *Room {
	return &Room{}
}

// Load 装载整个房间的目录树与正文。装载后语料不可变。
func (r *Room) Load(folders []Folder, docs []Document) error {
	if err := r.buildTree(folders); err != nil {
		return err
	}
	return r.buildIndex(docs)
}

// buildTree 校验并构建目录树，计算每个目录的子树文档数占位。
func (r *Room) buildTree(folders []Folder) error {
	r.folderIdx = make(map[int]int32, len(folders))
	r.folders = make([]*folderNode, len(folders))
	for i, f := range folders {
		if f.ID == 0 {
			return errors.New("fence: folder ID 0 is reserved")
		}
		if _, dup := r.folderIdx[f.ID]; dup {
			return fmt.Errorf("fence: duplicate folder ID %d", f.ID)
		}
		r.folderIdx[f.ID] = int32(i)
		r.folders[i] = &folderNode{id: f.ID, parent: -1}
	}
	for i, f := range folders {
		if f.ParentID == 0 {
			continue
		}
		p, ok := r.folderIdx[f.ParentID]
		if !ok {
			return fmt.Errorf("fence: folder %d has unknown parent %d", f.ID, f.ParentID)
		}
		r.folders[i].parent = p
		r.folders[p].children = append(r.folders[p].children, int32(i))
	}
	// 自根向下计算深度并校验上限；父指针均合法且每节点仅一个父，
	// 从根出发能覆盖全部连通节点，未覆盖的即为环。
	depth := make([]int, len(folders))
	stack := make([]int32, 0, len(folders))
	for i := range r.folders {
		if r.folders[i].parent == -1 {
			depth[i] = 1
			stack = append(stack, int32(i))
		}
	}
	visited := 0
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visited++
		for _, c := range r.folders[cur].children {
			depth[c] = depth[cur] + 1
			if depth[c] > maxTreeDepth {
				return fmt.Errorf("fence: folder %d exceeds max depth %d", r.folders[c].id, maxTreeDepth)
			}
			stack = append(stack, c)
		}
	}
	if visited != len(folders) {
		return errors.New("fence: folder tree contains a cycle")
	}
	return nil
}

// buildIndex 建立正文倒排索引与每文档词表。
func (r *Room) buildIndex(docs []Document) error {
	n := len(docs)
	r.folderOf = make([]int32, n)
	r.docIDs = make([]int, n)
	r.docTerms = make([][]uint32, n)
	r.docTFs = make([][]uint32, n)
	r.termIDs = make(map[string]uint32)
	r.postings = nil
	seen := make(map[int]struct{}, n)
	intern := func(w string) uint32 {
		if id, ok := r.termIDs[w]; ok {
			return id
		}
		id := uint32(len(r.termIDs))
		r.termIDs[w] = id
		r.postings = append(r.postings, nil)
		return id
	}
	counts := make(map[uint32]uint32, 64)
	for i, d := range docs {
		if _, dup := seen[d.ID]; dup {
			return fmt.Errorf("fence: duplicate document ID %d", d.ID)
		}
		seen[d.ID] = struct{}{}
		fi, ok := r.folderIdx[d.FolderID]
		if !ok {
			return fmt.Errorf("fence: document %d in unknown folder %d", d.ID, d.FolderID)
		}
		r.folderOf[i] = fi
		r.docIDs[i] = d.ID
		r.folders[fi].docs = append(r.folders[fi].docs, int32(i))
		for k := range counts {
			delete(counts, k)
		}
		for _, t := range tokenize(d.Text, intern) {
			counts[t]++
		}
		terms := make([]uint32, 0, len(counts))
		for t := range counts {
			terms = append(terms, t)
		}
		sort.Slice(terms, func(a, b int) bool { return terms[a] < terms[b] })
		tfs := make([]uint32, len(terms))
		for j, t := range terms {
			tfs[j] = counts[t]
			r.postings[t] = append(r.postings[t], posting{doc: uint32(i), tf: counts[t]})
		}
		r.docTerms[i] = terms
		r.docTFs[i] = tfs
	}
	r.computeSubDocs()
	r.users = make(map[int]*userState)
	r.scratchVis = make([]uint32, len(r.folders))
	r.scEpoch = make([]uint32, n)
	r.scScore = make([]uint64, n)
	r.scCnt = make([]uint32, n)
	return nil
}

// computeSubDocs 自底向上计算每个目录的子树文档总数。
func (r *Room) computeSubDocs() {
	order := make([]int32, 0, len(r.folders))
	stack := make([]int32, 0, len(r.folders))
	for i := range r.folders {
		if r.folders[i].parent == -1 {
			stack = append(stack, int32(i))
		}
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		order = append(order, cur)
		stack = append(stack, r.folders[cur].children...)
	}
	for i := len(order) - 1; i >= 0; i-- {
		node := r.folders[order[i]]
		node.subDocs = len(node.docs)
		for _, c := range node.children {
			node.subDocs += r.folders[c].subDocs
		}
	}
}
