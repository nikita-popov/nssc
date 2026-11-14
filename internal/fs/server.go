package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dustin/go-humanize"

	"nssc/internal/users"
)

// UserRoot returns the directory that backs a user's files.
//
// The canonical layout is <root>/user/<name>, which is what README documents
// and what `nssc adduser` creates. Instances created by older builds stored
// data directly in <root>/<name>; those are still found via the legacy
// fallback so an upgrade does not orphan existing files. New directories are
// always created at the canonical path.
func UserRoot(root, name string) string {
	canonical := filepath.Join(root, "user", name)
	if _, err := os.Stat(canonical); err != nil {
		legacy := filepath.Join(root, name)
		if info, lerr := os.Stat(legacy); lerr == nil && info.IsDir() {
			return legacy
		}
	}
	return canonical
}

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
	if err := os.MkdirAll(filepath.Join(root, "user"), 0755); err != nil {
		return nil, fmt.Errorf("failed to create user directory: %w", err)
	}
	server := &UserFSServer{
		root:        root,
		commonQuota: commonQuota,
		users:       make(map[string]*UserFS),
	}
	for _, user := range userList {
		userRoot := UserRoot(root, user.Name)
		if err := os.MkdirAll(userRoot, 0700); err != nil {
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
