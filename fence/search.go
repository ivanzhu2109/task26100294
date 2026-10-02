package fence

import "sort"

// maxQueryTerms 是查询去重后支持的词数上限（业务约定 1 至 4 个）。
const maxQueryTerms = 4

// topN 是返回的最大命中条数。
const topN = 20

// Search 在用户可见语料上检索 query，返回命中文档总数及得分最高的
// 前 20 条（得分降序，同分按文档ID升序）。查询词为连续英文字母、
// 忽略大小写；只有包含全部查询词的可见文档才算命中。
func (r *Room) Search(userID int, query string) (int, []Hit) {
	st := r.users[userID]
	if st == nil || st.visibleCnt == 0 {
		return 0, nil
	}
	terms := r.queryTerms(query)
	if len(terms) == 0 {
		return 0, nil
	}
	for _, t := range terms {
		if len(r.postings[t]) == 0 {
			return 0, nil
		}
	}
	costB := 0
	for _, t := range terms {
		costB += len(r.postings[t])
	}
	if len(terms) <= maxQueryTerms && st.visibleCnt*len(terms) <= costB {
		return r.searchByDocs(st, terms)
	}
	return r.searchByPostings(st, terms)
}

// queryTerms 把查询解析为去重后的词ID；不在词表中的词以 ^uint32(0) 占位。
func (r *Room) queryTerms(query string) []uint32 {
	var terms []uint32
	seen := make(map[uint32]struct{}, maxQueryTerms)
	lookup := func(w string) uint32 {
		id, ok := r.termIDs[w]
		if !ok {
			return ^uint32(0)
		}
		return id
	}
	for _, t := range tokenize(query, lookup) {
		if t == ^uint32(0) {
			return nil // 词表之外的词不可能命中
		}
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			terms = append(terms, t)
		}
	}
	return terms
}

// weights 按固定规则计算各查询词权重：可见文档总数 - 含该词的可见文档数 + 1。
func weights(visibleCnt int, df []uint32) [maxQueryTerms]uint64 {
	var w [maxQueryTerms]uint64
	for i, d := range df {
		w[i] = uint64(visibleCnt) - uint64(d) + 1
	}
	return w
}

// searchByDocs 枚举全部可见文档，逐一检查查询词成员关系。
// 适用于可见语料小而查询词在全库高频的场景。
func (r *Room) searchByDocs(st *userState, terms []uint32) (int, []Hit) {
	r.scratchEpoch++
	r.aDocs = r.aDocs[:0]
	r.aTF = r.aTF[:0]
	r.aMask = r.aMask[:0]
	stack := make([]int32, 0, 64)
	for _, g := range st.grants {
		stack = append(stack[:0], g)
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if r.scratchVis[cur] == r.scratchEpoch {
				continue
			}
			r.scratchVis[cur] = r.scratchEpoch
			for _, d := range r.folders[cur].docs {
				r.aDocs = append(r.aDocs, uint32(d))
				r.aTF = append(r.aTF, [4]uint32{})
				r.aMask = append(r.aMask, 0)
			}
			stack = append(stack, r.folders[cur].children...)
		}
	}
	var df [maxQueryTerms]uint32
	for di, d := range r.aDocs {
		dt, tf := r.docTerms[d], r.docTFs[d]
		for wi, t := range terms {
			if j, ok := findTerm(dt, t); ok {
				r.aTF[di][wi] = tf[j]
				r.aMask[di] |= 1 << uint(wi)
				df[wi]++
			}
		}
	}
	w := weights(st.visibleCnt, df[:])
	full := uint8(1)<<uint(len(terms)) - 1
	total := 0
	top := make([]Hit, 0, topN)
	for di, d := range r.aDocs {
		if r.aMask[di] != full {
			continue
		}
		var score uint64
		for wi := range terms {
			score += uint64(r.aTF[di][wi]) * w[wi]
		}
		total++
		top = insertTop(top, Hit{DocID: r.docIDs[d], Score: score})
	}
	return total, top
}

// searchByPostings 遍历查询词倒排表并按位图过滤可见文档。
// 适用于可见语料大而查询词稀有的场景。
func (r *Room) searchByPostings(st *userState, terms []uint32) (int, []Hit) {
	df := make([]uint32, len(terms))
	for wi, t := range terms {
		for _, p := range r.postings[t] {
			if st.visible(r.folderOf[p.doc]) {
				df[wi]++
			}
		}
	}
	r.searchEpoch++
	r.scTouched = r.scTouched[:0]
	for wi, t := range terms {
		w := uint64(st.visibleCnt) - uint64(df[wi]) + 1
		for _, p := range r.postings[t] {
			if !st.visible(r.folderOf[p.doc]) {
				continue
			}
			d := p.doc
			if r.scEpoch[d] != r.searchEpoch {
				r.scEpoch[d] = r.searchEpoch
				r.scScore[d] = 0
				r.scCnt[d] = 0
				r.scTouched = append(r.scTouched, d)
			}
			r.scCnt[d]++
			r.scScore[d] += uint64(p.tf) * w
		}
	}
	total := 0
	top := make([]Hit, 0, topN)
	for _, d := range r.scTouched {
		if r.scCnt[d] != uint32(len(terms)) {
			continue
		}
		total++
		top = insertTop(top, Hit{DocID: r.docIDs[d], Score: r.scScore[d]})
	}
	return total, top
}

// visible 报告内部目录序号是否在该用户的可见位图中。
func (st *userState) visible(f int32) bool {
	return st.vis[uint(f)>>6]>>(uint(f)&63)&1 == 1
}

// findTerm 在排序词表中二分查找词ID，返回下标与是否存在。
func findTerm(terms []uint32, t uint32) (int, bool) {
	i := sort.Search(len(terms), func(i int) bool { return terms[i] >= t })
	if i < len(terms) && terms[i] == t {
		return i, true
	}
	return i, false
}

// insertTop 把命中插入保持降序的前 20 名列表（同分按文档ID升序）。
func insertTop(top []Hit, h Hit) []Hit {
	i := sort.Search(len(top), func(i int) bool {
		if top[i].Score != h.Score {
			return top[i].Score < h.Score
		}
		return top[i].DocID > h.DocID
	})
	if i == topN {
		return top
	}
	if len(top) < topN {
		top = append(top, Hit{})
	}
	copy(top[i+1:], top[i:])
	top[i] = h
	return top
}
