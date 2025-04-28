package share_test

import (
	"os"
	"path/filepath"
	"testing"
	"nssc/internal/share"
)

func TestShareManager(t *testing.T) {
	publicDir := filepath.Join(os.TempDir(), "public")
	os.MkdirAll(publicDir, os.ModePerm)
	defer os.RemoveAll(publicDir)

	sm := share.NewShareManager(publicDir)

	t.Run("Create and remove share", func(t *testing.T) {
		testFile := filepath.Join(os.TempDir(), "test.txt")
		os.WriteFile(testFile, []byte("test"), 0644)

		link, err := sm.CreateShare(testFile)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(publicDir, link)); err != nil {
			t.Error("Symlink not created")
		}

		if err := sm.RemoveShare(link); err != nil {
			t.Error("Failed to remove share")
		}
	})
}
