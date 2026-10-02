package fence

import "sort"

// ReplaceGrants replaces the full grant set of one user. Granting a folder
// makes every document in that folder and all its descendants visible;
// the union of all granted scopes applies, with overlaps counted once.
// Unknown folder IDs are ignored. The next Search for this user uses the
// new scope; other users are unaffected.
func (r *Room) ReplaceGrants(userID int, folderIDs []int) {
	granted := make([]int32, 0, len(folderIDs))
	for _, id := range folderIDs {
		if fi, ok := r.folderIdx[id]; ok {
			granted = append(granted, fi)
		}
	}
	r.grants[userID] = granted

	nWords := (len(r.docIDs) + 63) / 64
	bitmap := make([]uint64, nWords)
	var docs []int32
	for _, fi := range granted {
		for _, d := range r.subtreeDocs[fi] {
			if bitmap[d/64]&(1<<(uint(d)%64)) == 0 {
				bitmap[d/64] |= 1 << (uint(d) % 64)
				docs = append(docs, d)
			}
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i] < docs[j] })
	r.vis[userID] = &visible{bitmap: bitmap, docs: docs}
}

// visibleOf returns the cached visible set for a user, or nil when the
// user has no grants (empty scope sees nothing).
func (r *Room) visibleOf(userID int) *visible {
	return r.vis[userID]
}
