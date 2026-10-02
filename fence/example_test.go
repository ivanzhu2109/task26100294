package fence_test

import (
	"fmt"

	"dataroom/fence"
)

func Example() {
	room := fence.NewRoom()
	err := room.Load(
		[]fence.Folder{
			{ID: 1, ParentID: -1}, // root
			{ID: 2, ParentID: 1},  // child of root
		},
		[]fence.Document{
			{ID: 101, FolderID: 1, Text: "Revenue grew steadily in 2024."},
			{ID: 102, FolderID: 2, Text: "Revenue revenue revenue!"},
		},
	)
	if err != nil {
		panic(err)
	}
	// User 7 may only see folder 2, so document 101 is invisible and
	// does not affect statistics or ranking.
	room.ReplaceGrants(7, []int{2})
	total, top := room.Search(7, "revenue")
	fmt.Println(total, top)
	// Output: 1 [{102 3}]
}
