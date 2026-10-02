package fence_test

import (
	"math/rand"
	"testing"

	"dataroom/fence"
)

// TestRandomizedVsReference builds random rooms and compares the library
// against the independent reference on random grants and queries.
func TestRandomizedVsReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	vocab := make([]string, 60)
	for i := range vocab {
		vocab[i] = alphaWord(i)
	}
	for trial := 0; trial < 30; trial++ {
		nFolders := 1 + rng.Intn(30)
		folders := make([]fence.Folder, nFolders)
		for i := range folders {
			parent := -1
			if i > 0 && rng.Intn(4) > 0 {
				parent = 1 + rng.Intn(i) // attach to an earlier folder (IDs are 1-based)
			}
			folders[i] = fence.Folder{ID: i + 1, ParentID: parent}
		}
		nDocs := 1 + rng.Intn(120)
		docs := make([]fence.Document, nDocs)
		for i := range docs {
			nWords := 1 + rng.Intn(12)
			words := make([]string, nWords)
			for j := range words {
				words[j] = vocab[rng.Intn(len(vocab))]
			}
			text := ""
			for j, w := range words {
				if j > 0 {
					text += []string{" ", ", ", "!"}[rng.Intn(3)]
				}
				text += w
			}
			docs[i] = fence.Document{ID: 1000 + i, FolderID: 1 + rng.Intn(nFolders), Text: text}
		}
		room := fence.NewRoom()
		if err := room.Load(folders, docs); err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		ref := newReference(folders, docs)
		for user := 0; user < 5; user++ {
			var queries []string
			for qi := 0; qi < 6; qi++ {
				n := 1 + rng.Intn(4)
				q := ""
				for k := 0; k < n; k++ {
					if k > 0 {
						q += " "
					}
					q += vocab[rng.Intn(len(vocab))]
				}
				queries = append(queries, q)
			}
			for round := 0; round < 4; round++ {
				var grants []int
				for g := 0; g < rng.Intn(6); g++ {
					grants = append(grants, 1+rng.Intn(nFolders))
				}
				check(t, room, ref, user, grants, queries)
			}
		}
	}
}

// TestLoadValidation checks structural errors are rejected.
func TestLoadValidation(t *testing.T) {
	room := fence.NewRoom()
	if err := room.Load([]fence.Folder{{ID: 1, ParentID: -1}, {ID: 1, ParentID: -1}}, nil); err == nil {
		t.Error("duplicate folder IDs accepted")
	}
	room = fence.NewRoom()
	if err := room.Load([]fence.Folder{{ID: 1, ParentID: 7}}, nil); err == nil {
		t.Error("unknown parent accepted")
	}
	room = fence.NewRoom()
	if err := room.Load([]fence.Folder{{ID: 1, ParentID: -1}},
		[]fence.Document{{ID: 1, FolderID: 1}, {ID: 1, FolderID: 1}}); err == nil {
		t.Error("duplicate document IDs accepted")
	}
	// Depth 11 must be rejected.
	folders := []fence.Folder{{ID: 0, ParentID: -1}}
	for i := 1; i <= 10; i++ {
		folders = append(folders, fence.Folder{ID: i, ParentID: i - 1})
	}
	room = fence.NewRoom()
	if err := room.Load(folders, nil); err == nil {
		t.Error("depth 11 accepted")
	}
	room = fence.NewRoom()
	if err := room.Load(folders[:10], nil); err != nil {
		t.Errorf("depth 10 rejected: %v", err)
	}
}
