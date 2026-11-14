package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"

	"golang.org/x/net/webdav"
)

// ErrReadOnlyRoot is returned for destructive operations that target the
// user's own root directory.
var ErrReadOnlyRoot = errors.New("operation not permitted on user root")

// maxSearchResults caps a single search so a pathological query cannot walk
// an arbitrarily large tree into memory.
const maxSearchResults = 1000

// UserFS is a per-user, quota-enforced view of one directory tree.
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

// resolvePath maps a client-supplied name to an absolute path inside the user
// root. Any explicit ".." path component is rejected, but legitimate names
// that merely start with dots (e.g. "..notes.txt") are allowed. Symlinks are
// resolved and the result is re-checked against the root, so a symlink cannot
// be used to escape it.
func (u *UserFS) resolvePath(name string) (string, error) {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return "", fs.ErrInvalid
		}
	}

	cleaned := path.Clean("/" + filepath.ToSlash(name))
	rel := strings.TrimPrefix(cleaned, "/")
	if rel == "" || rel == "." {
		return u.root, nil
	}

	fullPath := filepath.Join(u.root, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		// The file may not exist yet (WriteFile/Mkdir creating it); fall back
		// to a lexical check in that case.
		resolved = fullPath
	}
	relCheck, err := filepath.Rel(u.root, resolved)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fs.ErrInvalid
	}
	return fullPath, nil
}

// isRootPath reports whether name addresses the user root itself.
func isRootPath(name string) bool {
	cleaned := path.Clean("/" + filepath.ToSlash(name))
	return cleaned == "/"
}

// quotaCountWriter enforces a byte ceiling while copying an upload.
type quotaCountWriter struct {
	w       io.Writer
	limit   int64 // -1 means unlimited
	written int64
}

func (c *quotaCountWriter) Write(p []byte) (int, error) {
	if c.limit >= 0 && c.written+int64(len(p)) > c.limit {
		return 0, fmt.Errorf("user quota exceeded: %d bytes available", c.limit-c.written)
	}
	n, err := c.w.Write(p)
	c.written += int64(n)
	return n, err
}

// WriteFile creates or overwrites a file, charging quota for the bytes that
// actually landed on disk rather than for the client-declared size (sz may be
// -1 for chunked uploads, or simply lie). The content is written to a
// temporary file and renamed into place, so a failed upload never destroys the
// previous version of the file.
func (u *UserFS) WriteFile(name string, file io.Reader, sz int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	fullPath, err := u.resolvePath(name)
	if err != nil {
		return err
	}
	if fullPath == u.root {
		return ErrReadOnlyRoot
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	var oldSize int64
	if info, statErr := os.Stat(fullPath); statErr == nil {
		if info.IsDir() {
			return fmt.Errorf("cannot overwrite directory %q", name)
		}
		oldSize = info.Size()
	}

	limit := u.allowance(oldSize)
	if sz > 0 && limit >= 0 && sz > limit {
		return fmt.Errorf("user quota exceeded: need %d bytes, %d available", sz, limit)
	}

	tmp, err := os.CreateTemp(filepath.Dir(fullPath), ".nssc-upload-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	cw := &quotaCountWriter{w: tmp, limit: limit}
	if _, err := io.Copy(cw, file); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	u.updateQuotas(cw.written - oldSize)
	return nil
}

// allowance returns how many bytes may be written on top of oldSize before the
// user or common quota is exhausted. -1 means unlimited.
func (u *UserFS) allowance(oldSize int64) int64 {
	limit := int64(-1)
	if total, _, remain := u.quota.Values(); total > 0 {
		limit = remain + oldSize
	}
	if u.server != nil && u.server.commonQuota != nil {
		if total, _, remain := u.server.commonQuota.Values(); total > 0 {
			if l := remain + oldSize; limit < 0 || l < limit {
				limit = l
			}
		}
	}
	return limit
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

// quotaFile wraps an *os.File opened for writing and charges quota for the
// bytes that actually reach disk. It deliberately does NOT embed *os.File:
// embedding would promote ReadFrom and let io.Copy bypass Write entirely,
// which is exactly how quota accounting used to be skipped for WebDAV.
type quotaFile struct {
	f    *os.File
	ufs  *UserFS
	size int64 // current accounted size of the file
}

func (q *quotaFile) Read(p []byte) (int, error)              { return q.f.Read(p) }
func (q *quotaFile) Seek(o int64, whence int) (int64, error) { return q.f.Seek(o, whence) }
func (q *quotaFile) Readdir(n int) ([]fs.FileInfo, error)    { return q.f.Readdir(n) }
func (q *quotaFile) Stat() (fs.FileInfo, error)              { return q.f.Stat() }
func (q *quotaFile) Close() error                            { return q.f.Close() }

func (q *quotaFile) Write(p []byte) (int, error) {
	off, err := q.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	projected := off + int64(len(p))
	if projected > q.size {
		if err := q.ufs.checkQuotas(projected - q.size); err != nil {
			return 0, err
		}
	}
	n, werr := q.f.Write(p)
	if n > 0 {
		if newSize := off + int64(n); newSize > q.size {
			q.ufs.AddUsage(newSize - q.size)
			q.size = newSize
		}
	}
	return n, werr
}

// OpenFile opens a file with the given flags and permissions. Used by WebDAV
// and 9P. Write-mode opens are wrapped in quotaFile; O_TRUNC releases the old
// file's bytes from the quota immediately.
func (u *UserFS) OpenFile(ctx context.Context, path string, flag int, perm os.FileMode) (webdav.File, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	fullPath, err := u.resolvePath(path)
	if err != nil {
		log.Printf("Path %s open error: fs.ErrInvalid", path)
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	// Capture the size before opening: O_TRUNC empties the file, so a later
	// Stat would report 0 and the old bytes would never be released.
	var oldSize int64
	if info, statErr := os.Stat(fullPath); statErr == nil && !info.IsDir() {
		oldSize = info.Size()
	}

	f, err := os.OpenFile(fullPath, flag, perm)
	if err != nil {
		return nil, err
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) == 0 {
		return f, nil
	}

	var size int64
	if flag&os.O_TRUNC != 0 {
		if oldSize > 0 {
			u.updateQuotas(-oldSize)
		}
		size = 0
	} else if info, statErr := f.Stat(); statErr == nil {
		size = info.Size()
	}
	return &quotaFile{f: f, ufs: u, size: size}, nil
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

	var oldSize int64
	if info, statErr := os.Stat(fullPath); statErr == nil && !info.IsDir() {
		oldSize = info.Size()
	}
	f, err := os.OpenFile(fullPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return nil, err
	}
	if oldSize > 0 {
		u.updateQuotas(-oldSize)
	}
	return f, nil
}

// Remove removes a single file or empty directory. Used by 9P Tremove.
func (u *UserFS) Remove(ctx context.Context, path string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if isRootPath(path) {
		return ErrReadOnlyRoot
	}
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	size := info.Size()
	if info.IsDir() {
		size = u.calculateDirSize(fullPath)
	}
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
// Used by the 9P and WebDAV quota-enforcing writers.
func (u *UserFS) CheckQuota(size int64) error {
	return u.checkQuotas(size)
}

// AddUsage charges delta bytes to the user (and common) quota.
// Pass a negative delta when freeing space.
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

// MkdirAll creates a directory tree inside the user root.
func (u *UserFS) MkdirAll(ctx context.Context, path string, perm os.FileMode) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(fullPath, perm); err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// Mkdir creates a single directory inside the user root.
func (u *UserFS) Mkdir(ctx context.Context, path string, perm os.FileMode) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	if err := os.Mkdir(fullPath, perm); err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func (u *UserFS) Rename(ctx context.Context, oldName, newName string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if isRootPath(oldName) || isRootPath(newName) {
		return ErrReadOnlyRoot
	}
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

// RemoveAll removes a file or directory tree. The user root itself is
// protected: a request for "", "." or "/" is refused instead of wiping the
// whole account.
func (u *UserFS) RemoveAll(ctx context.Context, path string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if isRootPath(path) {
		return ErrReadOnlyRoot
	}
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return err
	}
	if fullPath == u.root {
		return ErrReadOnlyRoot
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
	u.updateQuotas(-size)
	return nil
}

// ReadDir lists directory contents. Protected by RLock to prevent races with RemoveAll.
func (u *UserFS) ReadDir(path string) ([]fs.DirEntry, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	fullPath, err := u.resolvePath(path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(fullPath)
}

// Search walks the user root and returns entries whose names match re.
// RelPath is returned as an absolute-looking slash path (leading "/") so the
// UI can build links as "/user" + RelPath. Results are capped at
// maxSearchResults.
func (u *UserFS) Search(re *regexp.Regexp) ([]FileEntry, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	results := make([]FileEntry, 0, 16)
	fs.WalkDir(os.DirFS(u.root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if p == "." {
			return nil
		}
		if re.MatchString(d.Name()) {
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			size := ""
			if !d.IsDir() {
				size = humanize.Bytes(uint64(info.Size()))
			}
			results = append(results, FileEntry{
				Name:    d.Name(),
				RelPath: "/" + filepath.ToSlash(p),
				IsDir:   d.IsDir(),
				Size:    size,
				ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
			})
			if len(results) >= maxSearchResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	return results, nil
}

// calculateDirSize returns the total size of regular files under path.
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
