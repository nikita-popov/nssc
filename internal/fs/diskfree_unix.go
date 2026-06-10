//go:build !windows

package fs

import "syscall"

// DiskFree returns free bytes on the filesystem hosting the server root.
func (s *UserFSServer) DiskFree() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
