package share_test

import (
	"nssc/internal/share"
	"os"
	"path/filepath"
	"testing"
)

func TestShareManager(t *testing.T) {
	publicDir := filepath.Join(os.TempDir(), "public")
	os.MkdirAll(publicDir, os.ModePerm)
	defer os.RemoveAll(publicDir)

	sm := share.NewShareManager(publicDir)

	t.Run("Create and remove share", func(t *testing.T) {
		// userRoot acts as the boundary; testFile is placed directly inside it.
		userRoot := os.TempDir()
		testFile := filepath.Join(userRoot, "test.txt")
		os.WriteFile(testFile, []byte("test"), 0644)
		defer os.Remove(testFile)

		link, err := sm.CreateShare(userRoot, "test.txt")
		if err != nil {
			t.Fatal(err)
		}

		if _, err := os.Lstat(filepath.Join(publicDir, link)); err != nil {
			t.Error("Symlink not created")
		}

		if err := sm.RemoveShare(link); err != nil {
			t.Error("Failed to remove share")
		}
	})
}
