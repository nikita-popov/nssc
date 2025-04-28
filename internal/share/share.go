package share

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type ShareManager struct {
	PublicDir string
}

func NewShareManager(publicDir string) *ShareManager {
	return &ShareManager{PublicDir: publicDir}
}

// CreateShare creates a symlink with UUIDv7 in public directory pointing to userFilePath.
func (sm *ShareManager) CreateShare(userFilePath string) (string, error) {
	uuid, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	id := uuid.String()
	linkPath := filepath.Join(sm.PublicDir, id)
	err = os.Symlink(userFilePath, linkPath)
	if err != nil {
		return "", err
	}
	return id, nil
}

// RemoveShare removes the symlink by UUID.
func (sm *ShareManager) RemoveShare(id string) error {
	linkPath := filepath.Join(sm.PublicDir, id)
	info, err := os.Lstat(linkPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("not a symlink")
	}
	return os.Remove(linkPath)
}
