package ninep

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The README documented "-9p unix:///run/nssc.sock" but styx.ListenAndServe
// always calls net.Listen("tcp", addr), so the path was parsed as a TCP port
// and the listener failed. Nothing covered it, so the bug shipped.
func TestListenUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "9p.sock")

	ln, err := (&Server{}).Listen("unix://" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("socket was not created: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("not a socket: %v", fi.Mode())
	}
	// The service runs with UMask=0077; without the explicit chmod the socket
	// would be 0700 and nobody but the service user could mount it.
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode = %04o, want 0660", perm)
	}

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("socket does not accept connections: %v", err)
	}
	c.Close()
}

func TestListenUnixSocketRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "9p.sock")

	ln, err := (&Server{}).Listen("unix://" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if _, err := (&Server{}).Listen("unix://" + path); err == nil {
		t.Fatal("second Listen on a live socket must fail, not steal it")
	}
}

func TestListenUnixSocketClearsStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "9p.sock")

	// What a killed instance leaves behind. Binding would fail with
	// "address already in use" if Listen did not clear it first.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := (&Server{}).Listen("unix://" + path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	ln.Close()
}

func TestListenTCP(t *testing.T) {
	ln, err := (&Server{}).Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	if ln.Addr().Network() != "tcp" {
		t.Fatalf("network = %q, want tcp", ln.Addr().Network())
	}
}
