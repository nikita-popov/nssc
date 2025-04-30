package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/dustin/go-humanize"

	"nssc/internal/users"
)

// Hold all UserFS's
type UserFSServer struct {
	root        string
	commonQuota *Quota
	users       map[string]*UserFS
	mu          sync.RWMutex
}

// Create new UserFSServer
func NewUserFSServer(root string, commonQuota *Quota, users []users.User) (*UserFSServer, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root directory: %w", err)
	}

	server := &UserFSServer{
		root:        root,
		commonQuota: commonQuota,
		users:       make(map[string]*UserFS),
	}

	for _, user := range users {
		userRoot := filepath.Join(root, user.Name)
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

		quota, err := humanize.ParseBytes(user.Quota)
		if err != nil {
			quota = 0
		}

		server.users[user.Name] = NewUserFS(
			userRoot,
			NewQuota(int64(quota)),
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

func (s *UserFSServer) DiskFree() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
