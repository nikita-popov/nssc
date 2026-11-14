package ninep_test

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"nssc/internal/fs"
	"nssc/internal/ninep"
	"nssc/internal/users"
)

// Minimal 9P2000 client. The point of these tests is the wire format itself:
// the server used to require a Tauth handshake that Linux v9fs and plan9port
// never send, which no Go-level test noticed because nothing spoke 9P here.
const (
	tversion = 100
	tattach  = 104
	rerror   = 107
	twalk    = 110
	tcreate  = 114
	twrite   = 118
	tclunk   = 120
)

type conn struct {
	t   *testing.T
	c   net.Conn
	tag uint16
}

func dial9P(t *testing.T, addr string) *conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &conn{t: t, c: c}
}

func (c *conn) send(typ byte, body []byte) {
	c.tag++
	msg := make([]byte, 7, 7+len(body))
	binary.LittleEndian.PutUint32(msg[0:], uint32(7+len(body)))
	msg[4] = typ
	binary.LittleEndian.PutUint16(msg[5:], c.tag)
	msg = append(msg, body...)
	if _, err := c.c.Write(msg); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// recv returns the reply type and body. A closed connection is an error, which
// is how a rejected session shows up on the wire.
func (c *conn) recv() (byte, []byte, error) {
	// A dead session may leave the socket open, so never block forever.
	if err := c.c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, nil, err
	}
	hdr := make([]byte, 7)
	if _, err := io.ReadFull(c.c, hdr); err != nil {
		return 0, nil, err
	}
	size := binary.LittleEndian.Uint32(hdr[0:])
	typ := hdr[4]
	body := make([]byte, size-7)
	if _, err := io.ReadFull(c.c, body); err != nil {
		return 0, nil, err
	}
	return typ, body, nil
}

func (c *conn) mustRecv(want byte) []byte {
	c.t.Helper()
	typ, body, err := c.recv()
	if err != nil {
		c.t.Fatalf("recv (want type %d): %v", want, err)
	}
	if typ == rerror {
		n := binary.LittleEndian.Uint16(body[0:])
		c.t.Fatalf("want type %d, got Rerror: %s", want, body[2:2+n])
	}
	if typ != want {
		c.t.Fatalf("want type %d, got %d", want, typ)
	}
	return body
}

func str(s string) []byte {
	b := make([]byte, 2, 2+len(s))
	binary.LittleEndian.PutUint16(b, uint16(len(s)))
	return append(b, s...)
}

func (c *conn) version() {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 8192)
	body = append(body, str("9P2000")...)
	c.send(tversion, body)
	c.mustRecv(101)
}

func (c *conn) attach(uname, aname string, fid uint32) (byte, []byte, error) {
	body := make([]byte, 8, 8+2+len(uname)+2+len(aname))
	binary.LittleEndian.PutUint32(body[0:], fid)
	binary.LittleEndian.PutUint32(body[4:], 0xFFFFFFFF) // NOFID: no Tauth
	body = append(body, str(uname)...)
	body = append(body, str(aname)...)
	c.send(tattach, body)
	return c.recv()
}

func (c *conn) clone(fid, newfid uint32) {
	body := make([]byte, 10)
	binary.LittleEndian.PutUint32(body[0:], fid)
	binary.LittleEndian.PutUint32(body[4:], newfid)
	binary.LittleEndian.PutUint16(body[8:], 0)
	c.send(twalk, body)
	c.mustRecv(111)
}

func (c *conn) create(fid uint32, name string) {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, fid)
	body = append(body, str(name)...)
	perm := make([]byte, 5)
	binary.LittleEndian.PutUint32(perm[0:], 0o644)
	perm[4] = 1 // OWRITE
	c.send(tcreate, append(body, perm...))
	c.mustRecv(115)
}

func (c *conn) write(fid uint32, off uint64, n int) (int, error) {
	body := make([]byte, 16, 16+n)
	binary.LittleEndian.PutUint32(body[0:], fid)
	binary.LittleEndian.PutUint64(body[4:], off)
	binary.LittleEndian.PutUint32(body[12:], uint32(n))
	body = append(body, make([]byte, n)...)
	c.send(twrite, body)
	typ, rb, err := c.recv()
	if err != nil {
		return 0, err
	}
	if typ == rerror {
		l := binary.LittleEndian.Uint16(rb[0:])
		return 0, &wireError{string(rb[2 : 2+l])}
	}
	return int(binary.LittleEndian.Uint32(rb[0:])), nil
}

func (c *conn) clunk(fid uint32) error {
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, fid)
	c.send(tclunk, body)
	typ, rb, err := c.recv()
	if err != nil {
		return err
	}
	if typ == rerror {
		l := binary.LittleEndian.Uint16(rb[0:])
		return &wireError{string(rb[2 : 2+l])}
	}
	return nil
}

type wireError struct{ msg string }

func (e *wireError) Error() string { return e.msg }

func newServer(t *testing.T, quota string) string {
	t.Helper()
	root := t.TempDir()
	db := &users.UsersDB{}
	if err := db.AddUser("alice", "secret123", quota); err != nil {
		t.Fatal(err)
	}
	fsSrv, err := fs.NewUserFSServer(root, nil, []users.User{{Name: "alice", Quota: quota}})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	srv := ninep.NewServer(db, fsSrv)
	go srv.Serve(l)
	return l.Addr().String()
}

// A client that never sends Tauth must still be able to attach. This is the
// exact traffic Linux v9fs produces, and it used to fail with "no such fid".
func TestAttachWithoutTauth(t *testing.T) {
	addr := newServer(t, "1GiB")
	c := dial9P(t, addr)
	c.version()

	typ, body, err := c.attach("alice", "secret123", 1)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if typ == rerror {
		n := binary.LittleEndian.Uint16(body[0:])
		t.Fatalf("attach rejected: %s", body[2:2+n])
	}
	if typ != 105 {
		t.Fatalf("attach: got type %d, want Rattach", typ)
	}
}

// Writes past the quota are refused with an error, and the session stays
// usable: the old writer closed the file, which killed the whole session.
func TestQuotaExceededKeepsSessionAlive(t *testing.T) {
	addr := newServer(t, "16KiB")
	c := dial9P(t, addr)
	c.version()
	if typ, _, err := c.attach("alice", "secret123", 1); err != nil || typ != 105 {
		t.Fatalf("attach: type=%d err=%v", typ, err)
	}

	c.clone(1, 2)
	c.create(2, "small.txt")
	if n, err := c.write(2, 0, 4096); err != nil || n != 4096 {
		t.Fatalf("write within quota: n=%d err=%v", n, err)
	}
	if err := c.clunk(2); err != nil {
		t.Fatalf("clunk: %v", err)
	}

	c.clone(1, 3)
	c.create(3, "big.txt")
	var off uint64
	var quotaErr error
	for i := 0; i < 8; i++ {
		n, err := c.write(3, off, 4096)
		if err != nil {
			quotaErr = err
			break
		}
		off += uint64(n)
	}
	if quotaErr == nil {
		t.Fatal("writes past the 16KiB quota were accepted")
	}
	if !strings.Contains(quotaErr.Error(), "permission") {
		t.Errorf("quota error = %q, want a permission error", quotaErr)
	}

	// The session must still answer. Tclunk and a zero-name Twalk are handled
	// by styx itself, so they prove nothing; Tcreate goes through the handler.
	c.clone(1, 4)
	c.create(4, "after.txt")
}

// A wrong password must not grant a working session.
func TestBadPasswordGetsNoSession(t *testing.T) {
	addr := newServer(t, "1GiB")
	c := dial9P(t, addr)
	c.version()

	// Rattach is sent before the handler runs, so the rejection shows up as a
	// closed connection on the next request.
	if _, _, err := c.attach("alice", "wrongpass", 1); err != nil {
		t.Fatalf("attach: %v", err)
	}
	body := make([]byte, 4)
	binary.LittleEndian.PutUint32(body, 1)
	body = append(body, str("gotcha.txt")...)
	perm := make([]byte, 5)
	binary.LittleEndian.PutUint32(perm[0:], 0o644)
	perm[4] = 1
	c.send(tcreate, append(body, perm...))
	typ, _, err := c.recv()
	if err == nil && typ == 115 {
		t.Fatal("a wrong password got a working session")
	}
}
