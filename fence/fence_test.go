package fence

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// refTokenize 用与库相同的规则切词，返回词频表。
func refTokenize(s string) map[string]uint32 {
	out := make(map[string]uint32)
	for _, w := range tokenize(s, func(w string) uint32 { return 0 }) {
		_ = w
	}
	// 直接用字符串版本，避免词ID干扰。
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out[lowerASCII(s[start:end])]++
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(s))
	return out
}

// refSearch 是独立参考实现：直接从各用户可见正文计算命中总数、得分与前20名。
func refSearch(docs []Document, visible func(docIdx int) bool, visibleCnt int, query string) (int, []Hit) {
	qterms := make(map[string]struct{})
	for w := range refTokenize(query) {
		qterms[w] = struct{}{}
	}
	if len(qterms) == 0 || visibleCnt == 0 {
		return 0, nil
	}
	terms := make([]string, 0, len(qterms))
	for w := range qterms {
		terms = append(terms, w)
	}
	df := make(map[string]uint32, len(terms))
	tfs := make([]map[string]uint32, len(docs))
	for i, d := range docs {
		if !visible(i) {
			continue
		}
		tfs[i] = refTokenize(d.Text)
		for _, w := range terms {
			if tfs[i][w] > 0 {
				df[w]++
			}
		}
	}
	total := 0
	var hits []Hit
	for i := range docs {
		if !visible(i) || tfs[i] == nil {
			continue
		}
		var score uint64
		ok := true
		for _, w := range terms {
			tf := tfs[i][w]
			if tf == 0 {
				ok = false
				break
			}
			score += uint64(tf) * (uint64(visibleCnt) - uint64(df[w]) + 1)
		}
		if !ok {
			continue
		}
		total++
		hits = append(hits, Hit{DocID: docs[i].ID, Score: score})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].DocID < hits[b].DocID
	})
	if len(hits) > topN {
		hits = hits[:topN]
	}
	return total, hits
}

// visibleSet 根据授权目录集合计算每篇文档的可见性（独立实现）。
func visibleSet(folders []Folder, docs []Document, grants []int) func(int) bool {
	children := make(map[int][]int)
	for _, f := range folders {
		children[f.ParentID] = append(children[f.ParentID], f.ID)
	}
	open := make(map[int]bool)
	var walk func(id int)
	walk = func(id int) {
		if open[id] {
			return
		}
		open[id] = true
		for _, c := range children[id] {
			walk(c)
		}
	}
	for _, g := range grants {
		walk(g)
	}
	return func(docIdx int) bool {
		return open[docs[docIdx].FolderID]
	}
}

func checkSearch(t *testing.T, r *Room, folders []Folder, docs []Document, userID int, grants []int, query string) {
	t.Helper()
	if err := r.ReplaceGrants(userID, grants); err != nil {
		t.Fatalf("ReplaceGrants: %v", err)
	}
	vis := visibleSet(folders, docs, grants)
	visibleCnt := 0
	for i := range docs {
		if vis(i) {
			visibleCnt++
		}
	}
	wantTotal, wantTop := refSearch(docs, vis, visibleCnt, query)
	gotTotal, gotTop := r.Search(userID, query)
	if gotTotal != wantTotal {
		t.Fatalf("total mismatch: got %d want %d (query %q grants %v)", gotTotal, wantTotal, query, grants)
	}
	if wantTop == nil {
		wantTop = []Hit{}
	}
	if gotTop == nil {
		gotTop = []Hit{}
	}
	if !reflect.DeepEqual(gotTop, wantTop) {
		t.Fatalf("top mismatch (query %q grants %v):\n got %v\nwant %v", query, grants, gotTop, wantTop)
	}
}

// TestOverlapGrantRevoke 覆盖祖孙授权重叠后撤去其中一项，
// 验证可见语料统计与结果均按剩余授权重新成立。
func TestOverlapGrantRevoke(t *testing.T) {
	folders := []Folder{
		{ID: 1, ParentID: 0},
		{ID: 2, ParentID: 1},
		{ID: 3, ParentID: 2},
		{ID: 4, ParentID: 1},
	}
	docs := []Document{
		{ID: 101, FolderID: 2, Text: "alpha alpha beta"},
		{ID: 102, FolderID: 3, Text: "alpha beta beta beta gamma"},
		{ID: 103, FolderID: 3, Text: "alpha gamma gamma"},
		{ID: 104, FolderID: 4, Text: "alpha beta delta"},
		{ID: 105, FolderID: 2, Text: "beta gamma"},
	}
	r := NewRoom()
	if err := r.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	// 祖孙重叠授权 [2,3]：可见 2、3 子树，文档 101/102/103/105。
	checkSearch(t, r, folders, docs, 7, []int{2, 3}, "alpha beta")
	// 撤去祖先授权，仅留 [3]：可见 102/103，统计范围随之缩小。
	checkSearch(t, r, folders, docs, 7, []int{3}, "alpha beta")
	checkSearch(t, r, folders, docs, 7, []int{3}, "alpha gamma")
	// 撤去子孙授权，仅留 [2]：仍可见 2 的全部子树（含 3）。
	checkSearch(t, r, folders, docs, 7, []int{2}, "alpha beta")
	// 重复授权不重复计数。
	checkSearch(t, r, folders, docs, 7, []int{2, 2, 3, 3}, "beta")
	// 空授权看不到任何文档。
	checkSearch(t, r, folders, docs, 7, nil, "alpha")
	// 未授权的兄弟分支不开放。
	checkSearch(t, r, folders, docs, 7, []int{4}, "alpha beta")
}

// TestHiddenBranchDoesNotLeak 两个用户搜索相同词；不可见分支中的
// 大量高词频文档不影响各自结果。
func TestHiddenBranchDoesNotLeak(t *testing.T) {
	folders := []Folder{
		{ID: 1, ParentID: 0},
		{ID: 2, ParentID: 1},
		{ID: 3, ParentID: 1},
		{ID: 4, ParentID: 1}, // 隐藏分支
	}
	docs := []Document{
		{ID: 201, FolderID: 2, Text: "apple banana apple"},
		{ID: 202, FolderID: 2, Text: "banana cherry"},
		{ID: 203, FolderID: 3, Text: "apple cherry cherry"},
		{ID: 204, FolderID: 3, Text: "apple banana"},
	}
	// 隐藏分支加入大量含查询词的高词频文档。
	for i := 0; i < 500; i++ {
		text := ""
		for j := 0; j < 50; j++ {
			text += "apple banana "
		}
		docs = append(docs, Document{ID: 1000 + i, FolderID: 4, Text: text})
	}
	withHidden := NewRoom()
	if err := withHidden.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	// 对照房间：完全相同，只是没有隐藏分支。
	noHidden := NewRoom()
	if err := noHidden.Load(folders, docs[:4]); err != nil {
		t.Fatal(err)
	}
	queries := []string{"apple", "apple banana", "cherry", "banana cherry"}
	for _, q := range queries {
		for _, tc := range []struct {
			user   int
			grants []int
		}{{11, []int{2}}, {22, []int{3}}, {33, []int{2, 3}}} {
			checkSearch(t, withHidden, folders, docs, tc.user, tc.grants, q)
			// 与无隐藏分支的房间结果逐项一致。
			if err := noHidden.ReplaceGrants(tc.user, tc.grants); err != nil {
				t.Fatal(err)
			}
			wantT, wantTop := noHidden.Search(tc.user, q)
			gotT, gotTop := withHidden.Search(tc.user, q)
			if gotT != wantT || !reflect.DeepEqual(gotTop, wantTop) {
				t.Fatalf("hidden branch leaked: user %d q %q got (%d,%v) want (%d,%v)",
					tc.user, q, gotT, gotTop, wantT, wantTop)
			}
		}
	}
}

// TestRandomizedAgainstReference 随机语料/授权/查询与参考实现逐项对拍，
// 同时覆盖按文档枚举与按倒排表两条检索路径。
func TestRandomizedAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	folders := []Folder{{ID: 1, ParentID: 0}}
	depth := []int{1}
	for i := 2; i <= 300; i++ {
		var p int
		for {
			p = 1 + rng.Intn(i-1)
			if depth[p-1] < 10 {
				break
			}
		}
		folders = append(folders, Folder{ID: i, ParentID: p})
		depth = append(depth, depth[p-1]+1)
	}
	words := make([]string, 200)
	for i := range words {
		words[i] = "w" + string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('a'+i/260))
	}
	docs := make([]Document, 1500)
	for i := range docs {
		n := 3 + rng.Intn(30)
		text := ""
		for j := 0; j < n; j++ {
			text += words[rng.Intn(len(words))] + " "
		}
		docs[i] = Document{ID: 5000 + i, FolderID: 1 + rng.Intn(300), Text: text}
	}
	r := NewRoom()
	if err := r.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	for user := 1; user <= 30; user++ {
		var grants []int
		if user%5 != 0 { // 每 5 个用户留一个空授权
			k := rng.Intn(8)
			for j := 0; j < k; j++ {
				grants = append(grants, 1+rng.Intn(300))
			}
		}
		for qi := 0; qi < 12; qi++ {
			nq := 1 + rng.Intn(4)
			q := ""
			for j := 0; j < nq; j++ {
				q += words[rng.Intn(len(words))] + " "
			}
			checkSearch(t, r, folders, docs, user, grants, q)
		}
	}
}

// TestTokenizer 校验切词规则：连续英文字母为词，忽略大小写。
func TestTokenizer(t *testing.T) {
	got := refTokenize("Hello, WORLD! foo-bar  x9Y _z_")
	want := map[string]uint32{"hello": 1, "world": 1, "foo": 1, "bar": 1, "x": 1, "y": 1, "z": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestLoadValidation 校验装载期的输入检查。
func TestLoadValidation(t *testing.T) {
	r := NewRoom()
	if err := r.Load([]Folder{{ID: 1}, {ID: 1}}, nil); err == nil {
		t.Fatal("expected duplicate folder error")
	}
	r = NewRoom()
	if err := r.Load([]Folder{{ID: 1, ParentID: 99}}, nil); err == nil {
		t.Fatal("expected unknown parent error")
	}
	r = NewRoom()
	folders := []Folder{{ID: 1}}
	for i := 2; i <= 11; i++ {
		folders = append(folders, Folder{ID: i, ParentID: i - 1})
	}
	if err := r.Load(folders, nil); err == nil {
		t.Fatal("expected depth error")
	}
	r = NewRoom()
	if err := r.Load([]Folder{{ID: 1}}, []Document{{ID: 1, FolderID: 1}, {ID: 1, FolderID: 1}}); err == nil {
		t.Fatal("expected duplicate doc error")
	}
	r = NewRoom()
	if err := r.Load([]Folder{{ID: 1}, {ID: 2, ParentID: 2}}, nil); err == nil {
		t.Fatal("expected cycle error")
	}
}
