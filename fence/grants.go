package fence

import "fmt"

// ReplaceGrants 用 folderIDs 整体替换某用户的授权范围。
// 授权一个目录即可见其全部子孙目录中的文档；多个授权取并集，
// 重复与祖孙重叠不会重复计数。替换后下一次检索即使用新范围。
func (r *Room) ReplaceGrants(userID int, folderIDs []int) error {
	st, ok := r.users[userID]
	if !ok {
		st = &userState{vis: make([]uint64, (len(r.folders)+63)/64)}
		r.users[userID] = st
	}
	for i := range st.vis {
		st.vis[i] = 0
	}
	st.grants = st.grants[:0]
	st.visibleCnt = 0
	seen := make(map[int32]struct{}, len(folderIDs))
	stack := make([]int32, 0, 64)
	for _, id := range folderIDs {
		fi, ok := r.folderIdx[id]
		if !ok {
			return fmt.Errorf("fence: grant of unknown folder %d", id)
		}
		if _, dup := seen[fi]; dup {
			continue
		}
		seen[fi] = struct{}{}
		st.grants = append(st.grants, fi)
	}
	for _, g := range st.grants {
		stack = append(stack[:0], g)
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			word, bit := uint(cur)>>6, uint(cur)&63
			if st.vis[word]>>bit&1 == 1 {
				continue // 整个子树已被其他授权覆盖
			}
			st.vis[word] |= 1 << bit
			st.visibleCnt += len(r.folders[cur].docs)
			stack = append(stack, r.folders[cur].children...)
		}
	}
	return nil
}
