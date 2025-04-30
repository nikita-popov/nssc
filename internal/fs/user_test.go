package fs_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"nssc/internal/fs"
	"nssc/internal/users"
)

func TestUserFSOperations(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	q := fs.NewQuota(1000000)
	db := &users.UsersDB{}
	db.AddUser("user", "pass", "1GiB")
	server, _ := fs.NewUserFSServer(t.TempDir(), q, db.Users)
	ufs := fs.NewUserFS(root, q, server)

	if err := ufs.MkdirAll(ctx, "testdir", 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	if _, err := ufs.Stat(ctx, "testdir"); err != nil {
		t.Errorf("Directory not created: %v", err)
	}

	f, err := ufs.OpenFile(ctx, "testfile", os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("File openning failed: %v", err)
	}
	f.Close()

	if err := ufs.WriteFile("testfile", bytes.NewReader([]byte("test content")), int64(len([]byte("test content")))); err != nil {
		t.Errorf("File writing failed: %v", err)
	}

	if info, err := ufs.Stat(ctx, "testfile"); err != nil || info.Size() != 12 {
		t.Errorf("File size mismatch: %d", info.Size())
	}

	if err := ufs.Rename(ctx, "testfile", "renamed"); err != nil {
		t.Errorf("Renaming failed: %v", err)
	}

	if _, err := ufs.Stat(ctx, "renamed"); err != nil {
		t.Errorf("File not renamed: %v", err)
	}
}

func TestQuotaEnforcement(t *testing.T) {
	root := t.TempDir()
	q := fs.NewQuota(100)
	db := &users.UsersDB{}
	db.AddUser("user", "pass", "1GiB")
	srv, _ := fs.NewUserFSServer(t.TempDir(), q, db.Users)
	ufs := fs.NewUserFS(root, q, srv)

	if err := ufs.WriteFile("large", bytes.NewReader(make([]byte, 150)), int64(150)); err == nil {
		t.Error("Expected quota exceeding error")
	}
}
