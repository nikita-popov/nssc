// Package ninep provides a 9P2000 file server backed by UserFSServer.
// Each session is authenticated against UsersDB and served from the
// corresponding per-user directory with quota enforcement.
package ninep

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path"

	"aqwari.net/net/styx"
	fsinternal "nssc/internal/fs"
	"nssc/internal/users"
)

// Server listens for 9P connections and dispatches sessions per user.
type Server struct {
	usersDB *users.UsersDB
	fsSrv   *fsinternal.UserFSServer
}

// NewServer creates a 9P server that authenticates against usersDB and
// serves files through fsSrv.
func NewServer(usersDB *users.UsersDB, fsSrv *fsinternal.UserFSServer) *Server {
	return &Server{
		usersDB: usersDB,
		fsSrv:   fsSrv,
	}
}

// ListenAndServe starts a 9P listener on addr (e.g. ":564" or
// "unix:///run/nssc.sock"). It blocks until the server fails.
func (s *Server) ListenAndServe(addr string) error {
	srv := &styx.Server{
		Addr:    addr,
		Handler: s,
		Auth:    s.authFunc(),
	}
	return srv.ListenAndServe()
}

// Serve starts a 9P server on an existing listener.
func (s *Server) Serve(l net.Listener) error {
	srv := &styx.Server{
		Handler: s,
		Auth:    s.authFunc(),
	}
	return srv.Serve(l)
}

func (s *Server) authFunc() styx.AuthFunc {
	return func(_ *styx.Channel, user, password string) error {
		if !s.usersDB.Authenticate(user, password) {
			return errors.New("authentication failed")
		}
		return nil
	}
}

// Serve9P handles a single 9P session for the authenticated user.
func (srv *Server) Serve9P(s *styx.Session) {
	ufs, err := srv.fsSrv.GetUserFS(s.User)
	if err != nil {
		log.Printf("9P: no filesystem for user %q: %v", s.User, err)
		return
	}

	ctx := context.Background()

	for s.Next() {
		switch msg := s.Request().(type) {

		case styx.Twalk:
			p := cleanPath(msg.Path())
			info, err := ufs.Stat(ctx, p)
			msg.Rwalk(info, err)

		case styx.Tstat:
			p := cleanPath(msg.Path())
			info, err := ufs.Stat(ctx, p)
			msg.Rstat(info, err)

		case styx.Topen:
			p := cleanPath(msg.Path())
			// fs.File is read-only; writable opens need OpenFile which
			// returns *os.File (satisfies io.ReadWriteCloser).
			if msg.Flag&os.O_WRONLY != 0 || msg.Flag&os.O_RDWR != 0 {
				f, err := ufs.OpenFile(ctx, p, msg.Flag, 0644)
				if err != nil {
					msg.Ropen(nil, err)
					continue
				}
				msg.Ropen(newQuotaWriter(f, ufs), nil)
			} else {
				f, err := ufs.Open(ctx, p)
				if err != nil {
					msg.Ropen(nil, err)
					continue
				}
				msg.Ropen(f, nil)
			}

		case styx.Tcreate:
			p := cleanPath(msg.NewPath())
			f, err := ufs.Create(ctx, p, msg.Mode)
			if err != nil {
				msg.Rcreate(nil, err)
				continue
			}
			msg.Rcreate(newQuotaWriter(f, ufs), nil)

		case styx.Tremove:
			p := cleanPath(msg.Path())
			msg.Rremove(ufs.Remove(ctx, p))

		case styx.Ttruncate:
			p := cleanPath(msg.Path())
			msg.Rtruncate(ufs.Truncate(ctx, p, msg.Size))

		case styx.Tutimes:
			p := cleanPath(msg.Path())
			msg.Rutimes(ufs.Chtimes(ctx, p, msg.Atime, msg.Mtime))

		default:
			log.Printf("9P: unhandled %T path=%s", msg, msg.Path())
		}
	}
}

// cleanPath normalises a client-supplied path to a relative slash-path.
// styx delivers absolute paths; stripping the leading slash makes them
// suitable for UserFS which expects paths relative to the user root.
func cleanPath(p string) string {
	return path.Clean("/" + p)[1:]
}

// quotaWriter wraps an io.ReadWriteCloser and charges written bytes to the
// user quota. If the quota is exceeded the write is rejected and the file
// is closed.
type quotaWriter struct {
	inner io.ReadWriteCloser
	ufs   *fsinternal.UserFS
}

func newQuotaWriter(rwc io.ReadWriteCloser, ufs *fsinternal.UserFS) *quotaWriter {
	return &quotaWriter{inner: rwc, ufs: ufs}
}

func (qw *quotaWriter) Read(p []byte) (int, error) { return qw.inner.Read(p) }
func (qw *quotaWriter) Close() error               { return qw.inner.Close() }

func (qw *quotaWriter) Write(p []byte) (int, error) {
	if err := qw.ufs.CheckQuota(int64(len(p))); err != nil {
		qw.inner.Close()
		return 0, fs.ErrPermission
	}
	written, err := qw.inner.Write(p)
	if written > 0 {
		qw.ufs.AddUsage(int64(written))
	}
	return written, err
}

// Ensure quotaWriter satisfies io.ReadWriteCloser at compile time.
var _ io.ReadWriteCloser = (*quotaWriter)(nil)
