//go:build windows

package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
		ufs.Init()
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

// DiskFree is not supported on Windows.
func (s *UserFSServer) DiskFree() (int64, error) {
	return 0, fmt.Errorf("DiskFree not supported on Windows")
}

// safeDirSize returns total size of regular files under path.
func safeDirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}
