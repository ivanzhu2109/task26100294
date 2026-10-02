package fence_test

import (
	"reflect"
	"sort"
	"testing"

	"dataroom/fence"
)

// alphaWord returns the i-th word of a pure-letter vocabulary
// ("baaa", "abaa", ... base-26 over four letters).
func alphaWord(i int) string {
	w := []byte("aaaa")
	for p := 3; p >= 0 && i > 0; p-- {
		w[p] += byte(i % 26)
		i /= 26
	}
	return string(w)
}

// refTokenize is an independent reimplementation of the word rules:
// runs of ASCII letters, case-insensitive.
func refTokenize(text string) []string {
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= 'a' && c <= 'z':
			cur = append(cur, c)
		case c >= 'A' && c <= 'Z':
			cur = append(cur, c+('a'-'A'))
		default:
			flush()
		}
	}
	flush()
	return words
}

// reference computes the expected result straight from each user's
// visible corpus, sharing no code with the library under test.
type reference struct {
	docs    map[int]string // doc ID -> text
	docFold map[int]int    // doc ID -> folder ID
	parent  map[int]int    // folder ID -> parent ID, roots absent
}

func newReference(folders []fence.Folder, docs []fence.Document) *reference {
	r := &reference{docs: map[int]string{}, docFold: map[int]int{}, parent: map[int]int{}}
	for _, f := range folders {
		if f.ParentID >= 0 {
			r.parent[f.ID] = f.ParentID
		}
	}
	for _, d := range docs {
		r.docs[d.ID] = d.Text
		r.docFold[d.ID] = d.FolderID
	}
	return r
}

func (r *reference) visible(grants []int) map[int]string {
	inScope := func(folder int) bool {
		for _, g := range grants {
			for f := folder; ; {
				if f == g {
					return true
				}
				p, ok := r.parent[f]
				if !ok {
					break
				}
				f = p
			}
		}
		return false
	}
	out := map[int]string{}
	for id, text := range r.docs {
		if inScope(r.docFold[id]) {
			out[id] = text
		}
	}
	return out
}

func (r *reference) search(grants []int, query string) (int, []fence.Hit) {
	vis := r.visible(grants)
	seen := map[string]bool{}
	var qwords []string
	for _, w := range refTokenize(query) {
		if !seen[w] {
			seen[w] = true
			qwords = append(qwords, w)
		}
	}
	if len(qwords) == 0 || len(vis) == 0 {
		return 0, nil
	}
	tf := make([]map[int]int, len(qwords))
	df := make([]int, len(qwords))
	for i := range qwords {
		tf[i] = map[int]int{}
	}
	for id, text := range vis {
		counts := map[string]int{}
		for _, w := range refTokenize(text) {
			counts[w]++
		}
		for qi, w := range qwords {
			if c := counts[w]; c > 0 {
				df[qi]++
				tf[qi][id] = c
			}
		}
	}
	total := 0
	var hits []fence.Hit
	for id := range vis {
		var score int64
		ok := true
		for qi := range qwords {
			c := tf[qi][id]
			if c == 0 {
				ok = false
				break
			}
			score += int64(c) * int64(len(vis)-df[qi]+1)
		}
		if !ok {
			continue
		}
		total++
		hits = append(hits, fence.Hit{DocID: id, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].DocID < hits[j].DocID
	})
	if len(hits) > 20 {
		hits = hits[:20]
	}
	return total, hits
}

// check compares the library against the reference for one user.
func check(t *testing.T, room *fence.Room, ref *reference, user int, grants []int, queries []string) {
	t.Helper()
	room.ReplaceGrants(user, grants)
	for _, q := range queries {
		wantTotal, wantTop := ref.search(grants, q)
		gotTotal, gotTop := room.Search(user, q)
		if gotTotal != wantTotal {
			t.Errorf("user %d query %q: total = %d, want %d", user, q, gotTotal, wantTotal)
		}
		if gotTop == nil && wantTop != nil {
			gotTop = []fence.Hit{}
		}
		if wantTop == nil && gotTop != nil {
			wantTop = []fence.Hit{}
		}
		if !reflect.DeepEqual(gotTop, wantTop) {
			t.Errorf("user %d query %q: top = %v, want %v", user, q, gotTop, wantTop)
		}
	}
}

// TestAncestorOverlapRevocation grants an ancestor and a descendant
// folder together, then revokes one of them; visible-corpus statistics
// and results must be recomputed from the remaining grant alone.
func TestAncestorOverlapRevocation(t *testing.T) {
	folders := []fence.Folder{
		{ID: 1, ParentID: -1},
		{ID: 2, ParentID: 1},
		{ID: 3, ParentID: 2},
		{ID: 4, ParentID: 1},
	}
	docs := []fence.Document{
		{ID: 10, FolderID: 1, Text: "alpha beta"},
		{ID: 11, FolderID: 2, Text: "alpha beta beta"},
		{ID: 12, FolderID: 3, Text: "alpha alpha alpha beta"},
		{ID: 13, FolderID: 3, Text: "beta gamma"},
		{ID: 14, FolderID: 4, Text: "alpha beta gamma delta"},
	}
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	ref := newReference(folders, docs)
	queries := []string{"alpha beta", "gamma", "alpha"}

	steps := [][]int{
		{2},    // subtree of 2: docs 11,12,13
		{2, 3}, // ancestor+descendant overlap, counted once
		{3},    // revoke ancestor: only docs 12,13 remain
		{2, 3}, // re-add overlap
		{2},    // revoke descendant
		{1, 3}, // root plus redundant descendant
		{},     // empty grant sees nothing
	}
	for _, grants := range steps {
		check(t, room, ref, 100, grants, queries)
	}
}

// TestHiddenBranchIsolation: two users search the same words; a branch
// invisible to both is then loaded with many high-frequency documents.
// Neither user's totals, scores or ranking may change.
func TestHiddenBranchIsolation(t *testing.T) {
	folders := []fence.Folder{
		{ID: 1, ParentID: -1},
		{ID: 2, ParentID: 1},
		{ID: 3, ParentID: 1},
	}
	docs := []fence.Document{
		{ID: 10, FolderID: 2, Text: "apple banana cherry"},
		{ID: 11, FolderID: 2, Text: "apple apple banana"},
		{ID: 12, FolderID: 3, Text: "apple banana banana banana"},
		{ID: 13, FolderID: 3, Text: "cherry apple"},
	}
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	ref := newReference(folders, docs)
	queries := []string{"apple banana", "apple", "banana cherry apple"}

	grantsA := []int{2}
	grantsB := []int{3}
	check(t, room, ref, 1, grantsA, queries)
	check(t, room, ref, 2, grantsB, queries)

	// Snapshot results before the hidden branch grows.
	snap := map[int]map[string]struct {
		total int
		top   []fence.Hit
	}{1: {}, 2: {}}
	for user := range map[int][]int{1: grantsA, 2: grantsB} {
		for _, q := range queries {
			tot, top := room.Search(user, q)
			snap[user][q] = struct {
				total int
				top   []fence.Hit
			}{tot, top}
		}
	}

	// Load a new room where invisible folder 4 holds many documents
	// stuffed with the query words at high frequency.
	folders2 := append(append([]fence.Folder{}, folders...), fence.Folder{ID: 4, ParentID: 1})
	docs2 := append([]fence.Document{}, docs...)
	for i := 0; i < 500; i++ {
		docs2 = append(docs2, fence.Document{ID: 1000 + i, FolderID: 4,
			Text: "apple apple apple apple apple banana banana banana cherry cherry"})
	}
	room2 := fence.NewRoom()
	if err := room2.Load(folders2, docs2); err != nil {
		t.Fatal(err)
	}
	room2.ReplaceGrants(1, grantsA)
	room2.ReplaceGrants(2, grantsB)
	for user, qs := range snap {
		for q, want := range qs {
			tot, top := room2.Search(user, q)
			if tot != want.total {
				t.Errorf("user %d query %q: total changed %d -> %d after hidden growth", user, q, want.total, tot)
			}
			if !reflect.DeepEqual(top, want.top) {
				t.Errorf("user %d query %q: top changed after hidden growth\n got %v\nwant %v", user, q, top, want.top)
			}
		}
	}
}

// TestEmptyAndEdge covers empty grants, unknown words, duplicate query
// words and separator handling.
func TestEmptyAndEdge(t *testing.T) {
	folders := []fence.Folder{{ID: 1, ParentID: -1}}
	docs := []fence.Document{
		{ID: 1, FolderID: 1, Text: "Hello, WORLD! hello123world x-y"},
	}
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	ref := newReference(folders, docs)
	queries := []string{
		"hello world",       // case-insensitive
		"hello123world",     // digits split words
		"x y",               // hyphen splits
		"hello hello world", // duplicates counted once
		"nosuchword",        // unknown word: no hits
		"!!!",               // no words at all
		"",                  // empty query
	}
	check(t, room, ref, 7, []int{1}, queries)
	check(t, room, ref, 8, nil, []string{"hello"}) // no grants
	check(t, room, ref, 9, []int{999}, []string{"hello"})
}

// TestGrantReplacementIsImmediate verifies a replaced scope applies to
// the very next search and does not leak across users.
func TestGrantReplacementIsImmediate(t *testing.T) {
	folders := []fence.Folder{
		{ID: 1, ParentID: -1},
		{ID: 2, ParentID: 1},
	}
	docs := []fence.Document{
		{ID: 1, FolderID: 1, Text: "shared words here"},
		{ID: 2, FolderID: 2, Text: "shared words too"},
	}
	room := fence.NewRoom()
	if err := room.Load(folders, docs); err != nil {
		t.Fatal(err)
	}
	ref := newReference(folders, docs)
	queries := []string{"shared words"}

	check(t, room, ref, 1, []int{1}, queries)
	check(t, room, ref, 2, []int{2}, queries)
	// User 1 loses folder 1, gains folder 2; user 2 must be unaffected.
	check(t, room, ref, 1, []int{2}, queries)
	check(t, room, ref, 2, []int{2}, queries)
	check(t, room, ref, 1, []int{1, 2}, queries)
	check(t, room, ref, 2, []int{2}, queries)
}
