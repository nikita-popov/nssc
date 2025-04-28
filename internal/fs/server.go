package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Hold all UserFS's
type UserFSServer struct {
	root        string
	commonQuota *Quota
	users       map[string]*UserFS
	mu          sync.RWMutex
}

// Create new UserFSServer
func NewUserFSServer(root string, commonQuota *Quota, users []string) (*UserFSServer, error) {
    if err := os.MkdirAll(root, 0755); err != nil {
        return nil, fmt.Errorf("failed to create root directory: %w", err)
    }

    server := &UserFSServer{
        root:        root,
        commonQuota: commonQuota,
        users:       make(map[string]*UserFS),
    }

    for _, username := range users {
        userRoot := filepath.Join(root, username)
        if err := os.MkdirAll(userRoot, 0755); err != nil {
            return nil, fmt.Errorf("failed to create user directory: %w", err)
        }

        var used int64
        filepath.Walk(userRoot, func(path string, info os.FileInfo, err error) error {
            if !info.IsDir() {
                used += info.Size()
            }
            return nil
        })

        server.users[username] = newUserFS(
            userRoot,
            NewQuota(0, used),
            server,
        )
    }

    return server, nil
}

// Return UserFS for username
func (s *UserFSServer) GetUserFS(username string) (*UserFS, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fs, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("fs for user %s not found", username)
	}
	return fs, nil
}

//
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

//
func (s *UserFSServer) DiskFree() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
