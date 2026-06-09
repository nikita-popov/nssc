package fs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/dustin/go-humanize"

	"nssc/internal/users"
)

// UserFSServer holds per-user UserFS instances.
type UserFSServer struct {
	root        string
	commonQuota *Quota
	users       map[string]*UserFS
	mu          sync.RWMutex
}

// NewUserFSServer initialises a UserFSServer and per-user directories.
// Quota usage is calculated inside each UserFS.Init() — no double counting.
func NewUserFSServer(root string, commonQuota *Quota, userList []users.User) (*UserFSServer, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root directory: %w", err)
	}
	server := &UserFSServer{
		root:        root,
		commonQuota: commonQuota,
		users:       make(map[string]*UserFS),
	}
	for _, user := range userList {
		userRoot := filepath.Join(root, user.Name)
		if err := os.MkdirAll(userRoot, 0755); err != nil {
			return nil, fmt.Errorf("failed to create user directory for %s: %w", user.Name, err)
		}
		quota, err := humanize.ParseBytes(user.Quota)
		if err != nil {
			quota = 0
		}
		ufs := NewUserFS(userRoot, NewQuota(int64(quota)), server)
		ufs.Init() // calculates initial used space; no pre-Walk needed
		server.users[user.Name] = ufs
	}
	return server, nil
}

// GetUserFS returns the UserFS for the given username.
func (s *UserFSServer) GetUserFS(username string) (*UserFS, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ufs, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("fs for user %s not found", username)
	}
	return ufs, nil
}

func (s *UserFSServer) checkCommonQuota(size int64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.commonQuota == nil {
		return nil
	}
	if total, _, remain := s.commonQuota.Values(); total > 0 && remain < size {
		return fmt.Errorf("common quota exceeded: remain %d < need %d", remain, size)
	}
	return nil
}

// DiskFree returns free bytes on the filesystem hosting root.
func (s *UserFSServer) DiskFree() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// safeDirSize returns total size of regular files under path.
// Uses fs.WalkDir to avoid the per-entry Lstat call of filepath.Walk.
func safeDirSize(path string) int64 {
	var size int64
	fs.WalkDir(os.DirFS(path), ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}
