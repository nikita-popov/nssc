//go:build windows

package fs

import "errors"

// DiskFree is not implemented on Windows.
func (s *UserFSServer) DiskFree() (int64, error) {
	return 0, errors.New("DiskFree not implemented on Windows")
}
