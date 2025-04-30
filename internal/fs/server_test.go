package fs_test

import (
	"path/filepath"
	"testing"

	"nssc/internal/fs"
	"nssc/internal/users"
)

func TestUserFSServer(t *testing.T) {
	root := t.TempDir()
	q := fs.NewQuota(1000)
	db := &users.UsersDB{}
	db.AddUser("testuser", "pass", "1GiB")
	server, _ := fs.NewUserFSServer(root, q, db.Users)

	ufs, err := server.GetUserFS("testuser")
	if err != nil {
		t.Fatalf("Ошибка получения UserFS: %v", err)
	}

	expectedPath := filepath.Join(root, "testuser")
	if ufs.Root() != expectedPath {
		t.Errorf("Некорректный корневой путь: %s", ufs.Root())
	}
}

func TestDiskFreeCalculation(t *testing.T) {
	db := &users.UsersDB{}
	db.AddUser("user", "pass", "1GiB")
	server, _ := fs.NewUserFSServer(t.TempDir(), fs.NewQuota(1000), db.Users)

	free, _ := server.DiskFree()
	if free <= 0 {
		t.Errorf("Некорректные значения свободного места: %+v", free)
	}
}
