package fs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"nssc/internal/fs"
	"nssc/internal/users"
)

func newTestUFS(t *testing.T, quotaBytes int64) (*fs.UserFS, string) {
	t.Helper()
	root := t.TempDir()
	db := &users.UsersDB{}
	if err := db.AddUser("alice", "pass", "1GiB"); err != nil {
		t.Fatal(err)
	}
	srv, err := fs.NewUserFSServer(t.TempDir(), nil, db.Users)
	if err != nil {
		t.Fatal(err)
	}
	ufs := fs.NewUserFS(root, fs.NewQuota(quotaBytes), srv)
	return ufs, root
}

// A name that merely starts with dots is a legal filename; only a real ".."
// path component is traversal.
func TestResolvePathAllowsDotDotPrefix(t *testing.T) {
	ufs, _ := newTestUFS(t, 1<<20)
	if err := ufs.WriteFile("..notes.txt", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("WriteFile(..notes.txt) failed: %v", err)
	}
	if _, err := ufs.Stat(context.Background(), "..notes.txt"); err != nil {
		t.Fatalf("Stat(..notes.txt) failed: %v", err)
	}
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	ufs, _ := newTestUFS(t, 1<<20)
	for _, name := range []string{"../escape", "a/../../escape", ".."} {
		if err := ufs.WriteFile(name, bytes.NewReader([]byte("x")), 1); err == nil {
			t.Errorf("WriteFile(%q) should have been rejected", name)
		}
	}
}

// Deleting the user root would wipe the whole account.
func TestRemoveAllRefusesRoot(t *testing.T) {
	ufs, root := newTestUFS(t, 1<<20)
	if err := ufs.WriteFile("keep.txt", bytes.NewReader([]byte("keep")), 4); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", ".", "/"} {
		err := ufs.RemoveAll(context.Background(), p)
		if !errors.Is(err, fs.ErrReadOnlyRoot) {
			t.Errorf("RemoveAll(%q) = %v, want ErrReadOnlyRoot", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("file was deleted along with the root: %v", err)
	}
}

// A streaming upload reports size -1; quota must be charged for the bytes that
// actually arrive, not for a bogus declared length.
func TestWriteFileChargesActualBytes(t *testing.T) {
	ufs, _ := newTestUFS(t, 1<<20)
	payload := bytes.Repeat([]byte("a"), 512)
	if err := ufs.WriteFile("stream.bin", bytes.NewReader(payload), -1); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	_, used, _ := ufs.GetQuota()
	if used != 512 {
		t.Errorf("used = %d, want 512", used)
	}
}

// Overwriting a file must not double-charge the quota.
func TestWriteFileOverwriteAccounting(t *testing.T) {
	ufs, _ := newTestUFS(t, 1<<20)
	if err := ufs.WriteFile("f.bin", bytes.NewReader(make([]byte, 400)), 400); err != nil {
		t.Fatal(err)
	}
	if err := ufs.WriteFile("f.bin", bytes.NewReader(make([]byte, 100)), 100); err != nil {
		t.Fatal(err)
	}
	_, used, _ := ufs.GetQuota()
	if used != 100 {
		t.Errorf("used = %d after overwrite, want 100", used)
	}
}

// Truncating on open must release the old bytes from the quota.
func TestOpenFileTruncReleasesQuota(t *testing.T) {
	ufs, _ := newTestUFS(t, 1<<20)
	ctx := context.Background()
	if err := ufs.WriteFile("t.bin", bytes.NewReader(make([]byte, 300)), 300); err != nil {
		t.Fatal(err)
	}
	f, err := ufs.OpenFile(ctx, "t.bin", os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, 50)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, used, _ := ufs.GetQuota()
	if used != 50 {
		t.Errorf("used = %d after truncate+write, want 50", used)
	}
}

func TestLegacyLayoutFallback(t *testing.T) {
	root := t.TempDir()

	// A legacy home with real content is still found.
	legacy := filepath.Join(root, "alice")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := fs.UserRoot(root, "alice"); got != legacy {
		t.Errorf("UserRoot = %q, want legacy %q", got, legacy)
	}

	// An empty canonical directory left behind by the old adduser must not
	// hide the data that actually sits at the legacy path.
	canonicalAlice := filepath.Join(root, "user", "alice")
	if err := os.MkdirAll(canonicalAlice, 0755); err != nil {
		t.Fatal(err)
	}
	if got := fs.UserRoot(root, "alice"); got != legacy {
		t.Errorf("UserRoot with empty canonical = %q, want legacy %q", got, legacy)
	}

	// Nothing legacy: canonical wins.
	canonical := filepath.Join(root, "user", "bob")
	if got := fs.UserRoot(root, "bob"); got != canonical {
		t.Errorf("UserRoot = %q, want canonical %q", got, canonical)
	}

	// Reserved names must never resolve to a shared directory.
	public := filepath.Join(root, "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "link"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := fs.UserRoot(root, "public"); got == public {
		t.Errorf("UserRoot for reserved name returned shared directory %q", got)
	}
}

func TestMigrateLegacyRoot(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "alice")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("payload")
	if err := os.WriteFile(filepath.Join(legacy, "f.txt"), payload, 0644); err != nil {
		t.Fatal(err)
	}
	// Empty canonical shell, exactly what the old adduser created.
	canonical := filepath.Join(root, "user", "alice")
	if err := os.MkdirAll(canonical, 0700); err != nil {
		t.Fatal(err)
	}

	server, err := fs.NewUserFSServer(root, nil, []users.User{{Name: "alice", Quota: "1GiB"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "f.txt")); err != nil {
		t.Fatalf("file not migrated to canonical path: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy directory still present: %v", err)
	}
	ufs, err := server.GetUserFS("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, used, _ := ufs.GetQuota(); used != int64(len(payload)) {
		t.Errorf("used = %d after migration, want %d", used, len(payload))
	}
}
