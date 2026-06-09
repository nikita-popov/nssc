// Package ninep provides a 9P file server stub.
// The full implementation is pending; only authentication is wired up.
// The styx dependency is kept so the import path resolves during build.
package ninep

import (
	"errors"
	"log"
	"net"
	"path"
	"sync"

	"aqwari.net/net/styx"
	"nssc/internal/users"
)

// UserFS holds per-user state for a 9P session.
type UserFS struct {
	userRoot string
	quota    int64
	mu       sync.Mutex
}

// Server listens for 9P connections and dispatches sessions per user.
type Server struct {
	listener net.Listener
	usersDB  *users.UsersDB
	rootDir  string
	fs       map[string]*UserFS
}

func NewServer(usersDB *users.UsersDB, rootDir string) *Server {
	return &Server{
		usersDB: usersDB,
		rootDir: rootDir,
		fs:      make(map[string]*UserFS),
	}
}

func (s *Server) authFunc() styx.AuthFunc {
	return func(_ *styx.Channel, user, access string) error {
		log.Printf("9P auth: user=%s", user)
		if !s.usersDB.Authenticate(user, access) {
			return errors.New("authentication failed")
		}
		return nil
	}
}

// Serve9P handles a single 9P session. Message handlers are not yet implemented.
func (srv *Server) Serve9P(s *styx.Session) {
	for s.Next() {
		msg := s.Request()
		file := path.Clean(msg.Path())
		log.Printf("9P: unhandled request type=%T path=%s", msg, file)
		// TODO: implement Twalk, Topen, Tstat, Tcreate, Tremove, Ttruncate, Tutimes
	}
}
