package fs

import (
	"fmt"
	"log"
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
	if dirHasData(canonical) {
		return canonical
	}
	// Nothing at the canonical path. Builds before the layout fix wrote to
	// <root>/<name>; follow that tree only when the name cannot collide with
	// a shared directory, and only when it actually holds something.
	if !isReservedName(name) && dirHasData(filepath.Join(root, name)) {
		return filepath.Join(root, name)
	}
	return canonical
}

// dirHasData reports whether path is a directory holding at least one entry.
// A directory that was created but never written to must not shadow real data
// sitting in the legacy location: that is exactly what old builds left behind.
func dirHasData(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// isReservedName reports whether name is also the name of a shared directory
// under the storage root. Treating <root>/user or <root>/public as a user home
// would expose every user's files.
func isReservedName(name string) bool {
	switch name {
	case "", ".", "..", "user", "public", "db.json":
		return true
	}
	return false
}

// migrateLegacyRoot moves a pre-layout-fix home from <root>/<name> to
// <root>/user/<name> when the canonical path holds no data yet. Renaming beats
// a merged view: afterwards there is exactly one tree, so quota accounting and
// the frontend see the same files. If both trees hold data the migration is
// skipped and the operator is told where the leftovers are.
func migrateLegacyRoot(root, name string) error {
	if isReservedName(name) {
		return nil
	}
	legacy := filepath.Join(root, name)
	if !dirHasData(legacy) {
		return nil
	}
	canonical := filepath.Join(root, "user", name)
	if dirHasData(canonical) {
		log.Printf("nssc: user %q has data in both %s and %s; using %s, legacy copy left untouched",
			name, canonical, legacy, canonical)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
		return fmt.Errorf("failed to create user directory: %w", err)
	}
	// The canonical path may exist as an empty shell; Rename refuses a
	// non-empty target, so drop the shell first.
	if info, err := os.Stat(canonical); err == nil && info.IsDir() {
		if err := os.Remove(canonical); err != nil {
			return fmt.Errorf("failed to replace empty directory %s: %w", canonical, err)
		}
	}
	if err := os.Rename(legacy, canonical); err != nil {
		return fmt.Errorf("failed to migrate legacy directory %s to %s: %w", legacy, canonical, err)
	}
	log.Printf("nssc: migrated legacy home of %q from %s to %s", name, legacy, canonical)
	return nil
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
		if err := migrateLegacyRoot(root, user.Name); err != nil {
			return nil, err
		}
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
