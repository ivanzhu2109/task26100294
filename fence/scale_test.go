package fence_test

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"dataroom/fence"
)

const (
	scaleDocs    = 200000
	scaleFolders = 20000
	scaleUsers   = 2000
	scaleVocab   = 20000
	scaleIters   = 1000
	scaleLimit   = 5 * time.Second

	nCommonWords = 8 // each in >= half of all docs
	nRareWords   = 8 // each in exactly 200 docs
	rareDocSpan  = 200
)

// scaleCorpus builds the 200k-document room: a folder tree of depth ~8,
// 10 documents per folder, ~32 words per document from a 20k vocabulary.
// Words 0..7 are common (>=50% doc frequency), words 8..15 are rare
// (exactly 200 docs each), the rest fill documents randomly.
func scaleCorpus(t *testing.T) ([]fence.Folder, []fence.Document, []int) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))

	folders := make([]fence.Folder, scaleFolders)
	for i := 0; i < scaleFolders; i++ {
		parent := -1
		if i > 0 {
			parent = (i-1)/4 + 1 // branching factor 4, 1-based IDs
		}
		folders[i] = fence.Folder{ID: i + 1, ParentID: parent}
	}

	vocab := make([]string, scaleVocab)
	for i := range vocab {
		vocab[i] = alphaWord(i)
	}

	docs := make([]fence.Document, 0, scaleDocs)
	words := make([]string, 0, 40)
	for i := 0; i < scaleDocs; i++ {
		words = words[:0]
		for c := 0; c < nCommonWords; c++ {
			if rng.Intn(10) < 6 { // ~60% doc frequency per common word
				words = append(words, vocab[c])
			}
		}
		if i < nRareWords*rareDocSpan {
			words = append(words, vocab[nCommonWords+i/rareDocSpan])
		}
		for len(words) < 32 {
			words = append(words, vocab[nCommonWords+nRareWords+rng.Intn(scaleVocab-nCommonWords-nRareWords)])
		}
		docs = append(docs, fence.Document{
			ID:       i + 1,
			FolderID: (i % scaleFolders) + 1,
			Text:     strings.Join(words, " "),
		})
	}

	// Subtree document counts per folder, for grant selection.
	subCount := make([]int, scaleFolders+1)
	for _, d := range docs {
		subCount[d.FolderID]++
	}
	for i := scaleFolders; i >= 1; i-- {
		if folders[i-1].ParentID > 0 {
			subCount[folders[i-1].ParentID] += subCount[i]
		}
	}
	return folders, docs, subCount
}

// TestScaleLoadA: users see at most 200 documents while every query word
// appears in at least half of the whole corpus. 1000 rounds of grant
// replacement plus search must finish within the limit.
func TestScaleLoadA(t *testing.T) {
	folders, docs, subCount := scaleCorpus(t)
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}

	var small []int
	for id := 1; id <= scaleFolders; id++ {
		if subCount[id] > 0 && subCount[id] <= 200 {
			small = append(small, id)
		}
	}
	if len(small) == 0 {
		t.Fatal("no small folders available")
	}
	rng := rand.New(rand.NewSource(1))

	start := time.Now()
	for i := 0; i < scaleIters; i++ {
		user := i % scaleUsers
		grants := []int{small[rng.Intn(len(small))]}
		room.ReplaceGrants(user, grants)
		n := 1 + rng.Intn(4)
		q := make([]string, n)
		for k := range q {
			q[k] = alphaWord(rng.Intn(nCommonWords))
		}
		total, top := room.Search(user, strings.Join(q, " "))
		if total > 200 {
			t.Fatalf("iter %d: total %d exceeds visible corpus", i, total)
		}
		if len(top) > 20 {
			t.Fatalf("iter %d: %d hits returned", i, len(top))
		}
	}
	elapsed := time.Since(start)
	t.Logf("load A: %d replace+search rounds in %v", scaleIters, elapsed)
	if elapsed > scaleLimit {
		t.Fatalf("load A too slow: %v > %v", elapsed, scaleLimit)
	}
}

// TestScaleLoadB: users see at least 90% of all documents while the query
// words together hit at most 200 documents in the whole corpus. 1000
// rounds of grant replacement plus search must finish within the limit.
func TestScaleLoadB(t *testing.T) {
	folders, docs, _ := scaleCorpus(t)
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(2))

	start := time.Now()
	for i := 0; i < scaleIters; i++ {
		user := i % scaleUsers
		// Root covers everything; add up to 63 redundant random grants
		// so grant combinations vary between rounds.
		grants := []int{1}
		for g := 0; g < rng.Intn(63); g++ {
			grants = append(grants, 1+rng.Intn(scaleFolders))
		}
		room.ReplaceGrants(user, grants)
		n := 1 + rng.Intn(4)
		q := make([]string, n)
		for k := range q {
			q[k] = alphaWord(nCommonWords + rng.Intn(nRareWords))
		}
		total, top := room.Search(user, strings.Join(q, " "))
		if total > nRareWords*rareDocSpan {
			t.Fatalf("iter %d: total %d exceeds rare-word corpus", i, total)
		}
		if len(top) > 20 {
			t.Fatalf("iter %d: %d hits returned", i, len(top))
		}
	}
	elapsed := time.Since(start)
	t.Logf("load B: %d replace+search rounds in %v", scaleIters, elapsed)
	if elapsed > scaleLimit {
		t.Fatalf("load B too slow: %v > %v", elapsed, scaleLimit)
	}
}

// TestScaleMemory loads the full room and checks the retained heap stays
// within the 512 MiB budget.
func TestScaleMemory(t *testing.T) {
	folders, docs, _ := scaleCorpus(t)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	// Touch every structure once: grants for all users, one search each.
	for u := 0; u < scaleUsers; u++ {
		room.ReplaceGrants(u, []int{1 + (u % scaleFolders)})
		room.Search(u, alphaWord(0)+" "+alphaWord(nCommonWords))
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	used := after.HeapAlloc - before.HeapAlloc
	t.Logf("room heap footprint: %.1f MiB (process total %.1f MiB)",
		float64(used)/1048576, float64(after.HeapAlloc)/1048576)
	const budget = 512 * 1048576
	if used > budget {
		t.Fatalf("room uses %.1f MiB, budget is 512 MiB", float64(used)/1048576)
	}
	runtime.KeepAlive(room)
}
