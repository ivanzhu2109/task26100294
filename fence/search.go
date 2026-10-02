package fence

import "sort"

// maxQueryWords is the maximum number of distinct query words.
const maxQueryWords = 4

// topN is the number of hits returned to the caller.
const topN = 20

// hitAcc accumulates per-document term frequencies for matched query words.
type hitAcc struct {
	mask uint8
	tf   [maxQueryWords]uint32
}

// Search returns the number of visible documents containing every distinct
// query word, plus the top 20 hits by score (descending; ties broken by
// ascending document ID). A word's weight is visibleTotal-df+1 where df
// counts visible documents containing the word.
func (r *Room) Search(userID int, query string) (int, []Hit) {
	vis := r.visibleOf(userID)
	if vis == nil || len(vis.docs) == 0 {
		return 0, nil
	}
	qterms := r.queryTerms(query)
	if len(qterms) == 0 {
		return 0, nil
	}

	df := make([]int, len(qterms))
	accs := make(map[int32]hitAcc)
	for qi, t := range qterms {
		lo, hi := r.postOff[t], r.postOff[t+1]
		if int(hi-lo) <= len(vis.docs) {
			r.scanPostings(vis, qi, lo, hi, accs, df)
		} else {
			r.scanVisible(vis, qi, t, accs, df)
		}
	}

	weights := make([]int64, len(qterms))
	for qi := range qterms {
		weights[qi] = int64(len(vis.docs)) - int64(df[qi]) + 1
	}
	full := uint8(1)<<uint(len(qterms)) - 1
	total := 0
	top := make([]Hit, 0, topN)
	for d, a := range accs {
		if a.mask != full {
			continue
		}
		total++
		var score int64
		for qi := range qterms {
			score += int64(a.tf[qi]) * weights[qi]
		}
		top = insertTop(top, Hit{DocID: r.docIDs[d], Score: score})
	}
	return total, top
}

// queryTerms maps query words to distinct term IDs, preserving first
// occurrence order. A word absent from the vocabulary makes the whole
// query unmatchable, reported by returning nil.
func (r *Room) queryTerms(query string) []uint32 {
	seen := map[uint32]struct{}{}
	var out []uint32
	for _, w := range tokenize(query) {
		t, ok := r.terms[w]
		if !ok {
			return nil
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// scanPostings walks one term's postings and scores visible documents.
func (r *Room) scanPostings(vis *visible, qi int, lo, hi uint32, accs map[int32]hitAcc, df []int) {
	for p := lo; p < hi; p++ {
		d := int32(r.postDoc[p])
		if vis.bitmap[d/64]&(1<<(uint(d)%64)) == 0 {
			continue
		}
		df[qi]++
		a := accs[d]
		a.mask |= 1 << uint(qi)
		a.tf[qi] = r.postTf[p]
		accs[d] = a
	}
}

// scanVisible walks the visible documents and looks the term up in each
// document's sorted term list.
func (r *Room) scanVisible(vis *visible, qi int, term uint32, accs map[int32]hitAcc, df []int) {
	for _, d := range vis.docs {
		lo, hi := r.docOff[d], r.docOff[d+1]
		i := sort.Search(int(hi-lo), func(i int) bool { return r.docTerm[lo+uint32(i)] >= term })
		if lo+uint32(i) >= hi || r.docTerm[lo+uint32(i)] != term {
			continue
		}
		df[qi]++
		a := accs[d]
		a.mask |= 1 << uint(qi)
		a.tf[qi] = r.docTf[lo+uint32(i)]
		accs[d] = a
	}
}

// insertTop keeps the best topN hits, ordered by score descending then
// document ID ascending.
func insertTop(top []Hit, h Hit) []Hit {
	i := sort.Search(len(top), func(i int) bool {
		if top[i].Score != h.Score {
			return top[i].Score < h.Score
		}
		return top[i].DocID > h.DocID
	})
	if i == len(top) && len(top) == topN {
		return top
	}
	top = append(top, Hit{})
	copy(top[i+1:], top[i:])
	top[i] = h
	if len(top) > topN {
		top = top[:topN]
	}
	return top
}
