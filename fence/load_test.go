package fence

import (
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"
)

// alphaWord 把非负整数编码为纯小写字母词，保证切词后仍是同一个词。
func alphaWord(i int) string {
	var buf [8]byte
	n := len(buf)
	for {
		n--
		buf[n] = byte('a' + i%26)
		if i < 26 {
			break
		}
		i = i/26 - 1
	}
	return string(buf[n:])
}

// commonWord 是第 k 个全库高频词。
func commonWord(k int) string { return alphaWord(loadFiller + k) }

// rareWord 是第 m 个稀有词（全库恰好 100 篇文档含有）。
func rareWord(m int) string { return alphaWord(loadFiller + loadCommon + m) }

const (
	loadDocs    = 200000
	loadFolders = 20000
	loadFiller  = 19500
	loadCommon  = 8
	loadRare    = 500
	firstLeaf   = 1250 // 目录 1250..20000 为叶子
)

// buildLoadCorpus 生成规模语料：20 万文档、2 万目录、约 2 万词表、
// 平均 32 词。8 个高频词各出现在约 60% 文档中；500 个稀有词各出现在
// 恰好 100 篇文档中。目录树为 16 叉树，深度约 4 层。
func buildLoadCorpus(t *testing.T) ([]Folder, []Document) {
	t.Helper()
	folders := make([]Folder, loadFolders)
	folders[0] = Folder{ID: 1}
	for i := 2; i <= loadFolders; i++ {
		folders[i-1] = Folder{ID: i, ParentID: 1 + (i-2)/16}
	}
	rng := rand.New(rand.NewSource(7))
	filler := make([]string, loadFiller)
	for i := range filler {
		filler[i] = alphaWord(i)
	}
	docs := make([]Document, loadDocs)
	leaves := loadFolders - firstLeaf + 1
	var sb strings.Builder
	for j := 0; j < loadDocs; j++ {
		sb.Reset()
		n := 0
		for k := 0; k < loadCommon; k++ {
			if rng.Float64() < 0.6 {
				sb.WriteString(commonWord(k))
				sb.WriteByte(' ')
				n++
			}
		}
		for n < 32 {
			sb.WriteString(filler[rng.Intn(loadFiller)])
			sb.WriteByte(' ')
			n++
		}
		docs[j] = Document{ID: 1000000 + j, FolderID: firstLeaf + j%leaves, Text: sb.String()}
	}
	// 每个稀有词恰好进入 100 篇不同文档。
	for m := 0; m < loadRare; m++ {
		w := rareWord(m)
		for i := 0; i < 100; i++ {
			d := (m*100 + i) * 4
			docs[d].Text += w + " "
		}
	}
	return folders, docs
}

func loadRoom(t *testing.T) (*Room, []Folder, []Document) {
	t.Helper()
	folders, docs := buildLoadCorpus(t)
	r := NewRoom()
	start := time.Now()
	if err := r.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	t.Logf("load took %v", time.Since(start))
	return r, folders, docs
}

func reportMem(t *testing.T) {
	t.Helper()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	used := ms.HeapInuse + ms.StackInuse
	t.Logf("heap+stack in use: %.1f MiB", float64(used)/(1<<20))
	if used > 512<<20 {
		t.Fatalf("memory %.1f MiB exceeds 512 MiB", float64(used)/(1<<20))
	}
}

// TestLoadSmallVisible 用户仅可见不超过 200 份文档，
// 查询词出现在全库至少一半文档中：1000 次替换授权并搜索 < 5 秒。
func TestLoadSmallVisible(t *testing.T) {
	r, folders, docs := loadRoom(t)
	reportMem(t)
	leaves := loadFolders - firstLeaf + 1
	// 先用参考实现锚定一次正确性。
	g0 := make([]int, 18)
	for i := range g0 {
		g0[i] = firstLeaf + i
	}
	checkSearch(t, r, folders, docs, 1, g0, commonWord(0)+" "+commonWord(1))

	start := time.Now()
	var sink int
	for it := 0; it < 1000; it++ {
		grants := make([]int, 18)
		base := (it * 18) % leaves
		for i := range grants {
			grants[i] = firstLeaf + (base+i)%leaves
		}
		if err := r.ReplaceGrants(1, grants); err != nil {
			t.Fatal(err)
		}
		nq := 1 + it%4
		q := ""
		for j := 0; j < nq; j++ {
			q += commonWord((it+j)%loadCommon) + " "
		}
		total, top := r.Search(1, q)
		sink += total + len(top)
	}
	d := time.Since(start)
	t.Logf("1000 replace+search (small visible) took %v, sink=%d", d, sink)
	if d > 5*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

// TestLoadLargeVisible 用户可见至少 90% 文档，
// 查询词在全库只命中不超过 200 份文档：1000 次替换授权并搜索 < 5 秒。
func TestLoadLargeVisible(t *testing.T) {
	r, folders, docs := loadRoom(t)
	reportMem(t)
	// 先用参考实现锚定一次正确性（可见全部文档）。
	checkSearch(t, r, folders, docs, 2, []int{1}, rareWord(0))

	start := time.Now()
	var sink int
	for it := 0; it < 1000; it++ {
		var grants []int
		if it%2 == 0 {
			grants = []int{1} // 根目录：100% 可见
		} else {
			// 根的 16 个子目录去掉 1 个：约 93.75% 可见。
			drop := it % 16
			for c := 0; c < 16; c++ {
				if c != drop {
					grants = append(grants, 2+c)
				}
			}
		}
		if err := r.ReplaceGrants(2, grants); err != nil {
			t.Fatal(err)
		}
		q := rareWord(it % loadRare)
		if it%3 == 0 {
			q += " " + rareWord((it+1)%loadRare)
		}
		total, top := r.Search(2, q)
		sink += total + len(top)
	}
	d := time.Since(start)
	t.Logf("1000 replace+search (large visible) took %v, sink=%d", d, sink)
	if d > 5*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}
