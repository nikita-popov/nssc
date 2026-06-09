package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"

	"golang.org/x/net/webdav"
)

// UserFS
type UserFS struct {
	root   string
	mu     sync.RWMutex
	tree   fs.FS
	quota  *Quota
	server *UserFSServer
}

func NewUserFS(root string, quota *Quota, server *UserFSServer) *UserFS {
	return &UserFS{
		root:   root,
		tree:   os.DirFS(root),
		quota:  quota,
		server: server,
	}
}

func (u *UserFS) Root() string { return u.root }

func (u *UserFS) resolvePath(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned == "/" || cleaned == "." || cleaned == "" {
		return u.root, nil
	}
	if strings.HasPrefix(cleaned, "/") {
		cleaned = strings.TrimPrefix(cleaned, "/")
	}
	if strings.HasPrefix(cleaned, "..") {
		return "", fs.ErrInvalid
	}
	fullPath := filepath.Join(u.root, cleaned)
	// Resolve symlinks to prevent path traversal via symlinks
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		// File may not exist yet (e.g. WriteFile creating new file) — fall back to lexical check
		resolved = fullPath
	}
	rel, err := filepath.Rel(u.root, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fs.ErrInvalid
	}
	return fullPath, nil
}

// WriteFile creates or overwrites a file, correctly accounting for quota on overwrite.
func (u *UserFS) WriteFile(name string, file io.Reader, sz int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	// Subtract existing file size from quota before overwrite
	var oldSize int64
	if info, err := os.Stat(fullPath); err == nil {
		oldSize = info.Size()
	}
	netDelta := sz - oldSize
	if err := u.checkQuotas(netDelta); err != nil {
		return err
	}
	dstFile, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	if _, err := io.Copy(dstFile, file); err != nil {
		return err
	}
	u.updateQuotas(netDelta)
	return nil
}

// Open opens a file for reading. Implements fs.FS.
func (u *UserFS) Open(ctx context.Context, path string) (fs.File, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		log.Printf("Path %s open error: fs.ErrInvalid", path)
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	return os.Open(fullPath)
}

// OpenFile opens a file with the given flags and permissions. Used by WebDAV.
func (u *UserFS) OpenFile(ctx context.Context, path string, flag int, perm os.FileMode) (webdav.File, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		log.Printf("Path %s open error: fs.ErrInvalid", path)
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	return os.OpenFile(fullPath, flag, perm)
}

// Create creates or truncates a file for reading and writing. Used by 9P Tcreate.
func (u *UserFS) Create(ctx context.Context, path string, perm os.FileMode) (*os.File, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: fs.ErrInvalid}
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(fullPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
}

// Remove removes a single file or empty directory. Used by 9P Tremove.
func (u *UserFS) Remove(ctx context.Context, path string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	size := info.Size()
	if err := os.Remove(fullPath); err != nil {
		return err
	}
	u.updateQuotas(-size)
	return nil
}

// Truncate truncates a file to the given size. Used by 9P Ttruncate.
func (u *UserFS) Truncate(ctx context.Context, path string, size int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	delta := size - info.Size()
	if delta > 0 {
		if err := u.checkQuotas(delta); err != nil {
			return err
		}
	}
	if err := os.Truncate(fullPath, size); err != nil {
		return err
	}
	u.updateQuotas(delta)
	return nil
}

// Chtimes updates access and modification times of a file. Used by 9P Tutimes.
func (u *UserFS) Chtimes(ctx context.Context, path string, atime, mtime time.Time) error {
	u.mu.RLock()
	defer u.mu.RUnlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	return os.Chtimes(fullPath, atime, mtime)
}

// CheckQuota returns an error if adding size bytes would exceed the user quota.
// Used by the 9P quota-enforcing writer.
func (u *UserFS) CheckQuota(size int64) error {
	return u.checkQuotas(size)
}

// AddUsage charges delta bytes to the user (and common) quota.
// Pass a negative delta when freeing space.
// Used by the 9P quota-enforcing writer.
func (u *UserFS) AddUsage(delta int64) {
	u.updateQuotas(delta)
}

// Stat returns file info. Implements fs.StatFS.
func (u *UserFS) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	fullPath, err := u.resolvePath(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return info, nil
}

// MkdirAll
func (u *UserFS) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(fullPath, perm); err != nil {
		return &fs.PathError{
			Op:   "mkdir",
			Path: path,
			Err:  err,
		}
	}
	return nil
}

// Mkdir
func (u *UserFS) Mkdir(ctx context.Context, path string, perm os.FileMode) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	log.Printf("Full path: %s", fullPath)
	if err := os.Mkdir(fullPath, perm); err != nil {
		return &fs.PathError{
			Op:   "mkdir",
			Path: path,
			Err:  err,
		}
	}
	return nil
}

func (u *UserFS) Rename(ctx context.Context, oldName, newName string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	oldPath, err := u.resolvePath(oldName)
	if err != nil {
		return err
	}
	newPath, err := u.resolvePath(newName)
	if err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

// RemoveAll removes a file or directory tree.
func (u *UserFS) RemoveAll(ctx context.Context, path string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	var size int64
	if info.IsDir() {
		size = u.calculateDirSize(fullPath)
	} else {
		size = info.Size()
	}
	if err := os.RemoveAll(fullPath); err != nil {
		return err
	}
	u.quota.AddUsage(-size)
	if u.server.commonQuota != nil {
		u.server.commonQuota.AddUsage(-size)
	}
	return nil
}

func (u *UserFS) ReadDir(path string) ([]fs.DirEntry, error) {
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(fullPath)
}

// Search walks the user root and returns entries whose names match re.
// Uses fs.WalkDir to avoid the per-entry Lstat call of filepath.Walk.
func (u *UserFS) Search(re *regexp.Regexp) ([]FileEntry, error) {
	var results []FileEntry
	fs.WalkDir(os.DirFS(u.root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if re.MatchString(d.Name()) {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			size := ""
			if !d.IsDir() {
				size = humanize.Bytes(uint64(info.Size()))
			}
			results = append(results, FileEntry{
				Name:    d.Name(),
				RelPath: path,
				IsDir:   d.IsDir(),
				Size:    size,
				ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
			})
		}
		return nil
	})
	return results, nil
}

// calculateDirSize returns the total size of regular files under path.
// Uses fs.WalkDir to avoid the per-entry Lstat call of filepath.Walk.
func (u *UserFS) calculateDirSize(path string) int64 {
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

func (u *UserFS) GetQuota() (int64, int64, int64) {
	return u.quota.Values()
}

func (u *UserFS) checkQuotas(size int64) error {
	if total, _, remain := u.quota.Values(); total > 0 && remain < size {
		return fmt.Errorf("user quota exceeded: remain %d < need %d", remain, size)
	}
	if u.server != nil {
		return u.server.checkCommonQuota(size)
	}
	return nil
}

func (u *UserFS) updateQuotas(size int64) {
	u.quota.AddUsage(size)
	if u.server != nil && u.server.commonQuota != nil {
		u.server.commonQuota.AddUsage(size)
	}
}

func (u *UserFS) Init() {
	used := u.calculateDirSize(u.root)
	u.quota.AddUsage(used)
}

func (u *UserFS) FS() fs.FS {
	return u.tree
}

func (u *UserFS) Sub(dir string) (fs.FS, error) {
	return fs.Sub(u.tree, dir)
}
