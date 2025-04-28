package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/net/webdav"
)

// UserFS
type UserFS struct {
	ctx      context.Context
	root     string
	mu       sync.RWMutex
	tree     fs.FS
	quota    *Quota
	server   *UserFSServer
}

//
func newUserFS(root string, quota *Quota, server *UserFSServer) *UserFS {
	return &UserFS{
		root:   root,
		tree:   os.DirFS(root),
		quota:  quota,
		server: server,
	}
}

//
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

	fullPath := filepath.Join(u.root, name)

	rel, err := filepath.Rel(u.root, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fs.ErrInvalid
	}

	return fullPath, nil
}

// WriteFile create or re-write file
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

    if err := u.checkQuotas(sz); err != nil {
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

	u.updateQuotas(sz)
	return nil
}

// For fs.FS interface
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

// For fs.FS WebDAV interface
func (u *UserFS) OpenFile(ctx context.Context, path string, flag int, perm os.FileMode) (webdav.File, error) {
    u.mu.RLock()
    defer u.mu.RUnlock()

	fullPath, err := u.resolvePath(path)
    if err != nil {
		log.Printf("Path %s open error: fs.ErrInvalid", path)
        return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
    }

	/* TODO:
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 || flag&os.O_APPEND != 0 {
		if fs.quota > 0 {
			if currentUsage := fs.getCurrentUsage(); currentUsage >= fs.quota {
				return nil, webdav.ErrLocked
			}
		}
	}*/

    return os.OpenFile(fullPath, flag, perm)
}

// For fs.StatFS interface
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

//
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

// RemoveFile
func (u *UserFS) RemoveAll(ctx context.Context, name string) error {
    u.mu.Lock()
    defer u.mu.Unlock()

    fullPath, err := u.resolvePath(name)
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

//
func (u *UserFS) ReadDir(path string) ([]fs.DirEntry, error) {
	fullPath, err := u.resolvePath(path)
    if err != nil {
        return nil, err
    }
	return os.ReadDir(fullPath)
}

//
func (u *UserFS) calculateDirSize(path string) int64 {
    var size int64
    filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
        if !info.IsDir() {
            size += info.Size()
        }
        return nil
    })
    return size
}

//
func (u *UserFS) GetQuota() (int64, int64, int64) {
	return u.quota.Values()
}

//
func (u *UserFS) checkQuotas(size int64) error {
    if total, _, remain := u.quota.Values(); total > 0 && remain < size {
        return fmt.Errorf("user quota exceeded: remain %d < need %d", remain, size)
    }

    if u.server != nil {
        return u.server.checkCommonQuota(size)
    }
    return nil
}

//
func (u *UserFS) updateQuotas(size int64) {
    u.quota.AddUsage(size)
    if u.server != nil && u.server.commonQuota != nil {
        u.server.commonQuota.AddUsage(size)
    }
}

//
func (u *UserFS) FS() fs.FS {
    return u.tree
}

//
func (u *UserFS) Sub(dir string) (fs.FS, error) {
    return fs.Sub(u.tree, dir)
}
