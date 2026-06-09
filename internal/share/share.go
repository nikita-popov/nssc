package share

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ShareManager creates and removes public symlinks for shared files.
type ShareManager struct {
	PublicDir string
}

func NewShareManager(publicDir string) *ShareManager {
	return &ShareManager{PublicDir: publicDir}
}

// CreateShare validates that relPath is inside userRoot, then creates a
// symlink with a UUIDv7 name in PublicDir pointing to the resolved absolute path.
// PublicDir is created on first use if it does not exist.
func (sm *ShareManager) CreateShare(userRoot, relPath string) (string, error) {
	abs := filepath.Join(userRoot, relPath)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("share target not found: %w", err)
	}
	// Ensure the resolved path is still inside userRoot.
	cleanRoot := filepath.Clean(userRoot)
	if !strings.HasPrefix(resolved, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside user root")
	}

	// Create PublicDir lazily so a fresh data directory works out of the box.
	if err := os.MkdirAll(sm.PublicDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create public dir: %w", err)
	}

	uid, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	id := uid.String()
	linkPath := filepath.Join(sm.PublicDir, id)
	if err := os.Symlink(resolved, linkPath); err != nil {
		return "", fmt.Errorf("failed to create share symlink: %w", err)
	}
	return id, nil
}

// RemoveShare removes the symlink identified by id.
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
