package ninep

import (
    "context"
	"errors"
    "log"
    "net"
    //"os"
	"path"
    //"path/filepath"
    //"strings"
    "sync"

    "aqwari.net/net/styx"
    //"aqwari.net/net/styx/styxauth"
    "nssc/internal/users"
)

type UserFS struct {
    userRoot string
    quota    int64
    mu       sync.Mutex
}

type Server struct {
    listener net.Listener
    usersDB  *users.UsersDB
    rootDir  string
    //fsCache  *sync.Map // map[string]*UserFS
	fs       map[string]*UserFS
	ctx      context.Context
}

func NewServer(usersDB *users.UsersDB, rootDir string) *Server {
    return &Server{
        usersDB: usersDB,
        rootDir: rootDir,
        //fsCache: &sync.Map{},
    }
}

/*func (s *Server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = listener

	server := styx.Server{
		Auth: s.authFunc(),
		Handler: styx.HandlerFunc(func(session *styx.Session) {
			user := session.User()
			fs, _ := s.fsCache.LoadOrStore(user, &UserFS{
				userRoot: filepath.Join(s.rootDir, "users", user),
				quota:    s.usersDB.GetQuota(user),
			})
			fs.(*UserFS).Serve9P(session)
		}),
	}

	go server.Serve(s.listener)
	return nil
    }*/

func (s *Server) authFunc() styx.AuthFunc {
	return func(_ *styx.Channel, user, access string) error {
		log.Printf("user: %s, access: %s", user, access)
		if !s.usersDB.Authenticate(user, access) {
			return errors.New("authentication failed")
		}
		return nil
	}
}

func (srv *Server) Serve9P(s *styx.Session) {
	//Loop:
	for s.Next() {
		msg := s.Request()
		file := path.Clean(msg.Path())
		log.Println("Handling: ", file)

		// Switch on the kind of message we are receiving, not all will arrive here and are handled by styx
		// Only every give styx a VFile to ensure it can cast interfaces correctly
		switch t := msg.(type) {
		case styx.Twalk:
			log.Println("=== walk: ", t)
			//f, err := srv.fs.lookup(*srv, file)
			//t.Rwalk(f.VF(), err)

		case styx.Topen:
			log.Println("=== open: ", t)
			//f, err := srv.userFS.handleWalk(*srv, file)
			//t.Ropen(f.VF(), err)

		case styx.Tstat:
			log.Println("=== stat: ", t)
			//f, err := srv.userFS.handleWalk(*srv, file)
			//t.Rstat(f.VF(), err)

		case styx.Tcreate:
			log.Println("=== create: ", t)
			// TODO - something special for directories?
			//full := file + t.Name

			// Insert into file tree
			//f, err := srv.File.Insert(full, false)
			//if err != nil {
			//	t.Rerror("tree insert failed %s", err)
			//	continue Loop
			//}

			// Upload to blob storage
			//err = f.Blob.Upload(srv.ctx)
			//if err != nil {
			//	t.Rerror("azure upload failed %s", err)
			//	continue Loop
			//}

			//t.Rcreate(f.VF(), nil)

		case styx.Tremove:
			log.Println("=== rm: ", t)
			//full := t.Path()
			//f, err := srv.userFS.lookup(*srv, full)

			//err = srv.File.Delete(full)
			//if err != nil {
			//	t.Rerror("azure delete failed %s", err)
			//}

			//t.Rremove(err)

		case styx.Ttruncate:
			// TODO
			log.Println("=== truncate?: ", t)

		case styx.Tutimes:
			// Change last modified time
			log.Println("=== utimes?: ", t)
			// TODO
			//t.Rutimes(nil)

		}
	}
}

/*func (fs *UserFS) lookup(srv Server, full string) (*File, error) {
	// Sync root
	srv.File.Sync()

	cleaned := path.Clean(full)

	// Short circuit base case for root
	if cleaned == "/" {
		return srv.File, nil
	}

	f, err := srv.File.Search(cleaned)
	if f == nil {
		return srv.File, err
	}

	return f, err
}

func (fs *UserFS) handleOpen(req styx.Topen, path string) {
    file, err := os.Open(path)
    if err != nil {
        req.Ropen(nil, styx.EIO)
        return
    }
    defer file.Close()

    req.Ropen(styx.OSFile(path), nil)
}

func (fs *UserFS) handleRead(req styx.Tread, path string) {
    file, err := os.Open(path)
    if err != nil {
        req.Rread(nil, styx.EIO)
        return
    }
    defer file.Close()

    buf := make([]byte, req.Count())
    n, err := file.ReadAt(buf, req.Offset())
    if err != nil && err != io.EOF {
        req.Rread(nil, styx.EIO)
        return
    }

    req.Rread(buf[:n], nil)
}

func (fs *UserFS) handleWrite(req styx.Twrite, path string) {
    fs.mu.Lock()
    defer fs.mu.Unlock()

    if fs.quota > 0 {
        if current := fs.currentUsage(); current+int64(len(req.Data())) > fs.quota {
            req.Rwrite(0, styx.Enospace)
            return
        }
    }

    file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
    if err != nil {
        req.Rwrite(0, styx.EIO)
        return
    }
    defer file.Close()

    n, err := file.WriteAt(req.Data(), req.Offset())
    if err != nil {
        req.Rwrite(0, styx.EIO)
        return
    }

    req.Rwrite(n, nil)
}

func (fs *UserFS) handleCreate(req styx.Tcreate, path string) {
    fs.mu.Lock()
    defer fs.mu.Unlock()

    if req.Dir() {
        if err := os.MkdirAll(path, os.FileMode(req.Perm())); err != nil {
            req.Rcreate(nil, styx.EIO)
            return
        }
    } else {
        file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, os.FileMode(req.Perm()))
        if err != nil {
            req.Rcreate(nil, styx.Eexist)
            return
        }
        file.Close()
    }

    req.Rcreate(styx.OSFile(path), nil)
}

func (fs *UserFS) handleRemove(req styx.Tremove, path string) {
    if err := os.RemoveAll(path); err != nil {
        req.Rremove(styx.EIO)
        return
    }
    req.Rremove(nil)
}

func (fs *UserFS) currentUsage() int64 {
    var total int64
    filepath.Walk(fs.userRoot, func(path string, info os.FileInfo, err error) error {
        if !info.IsDir() {
            total += info.Size()
        }
        return nil
    })
    return total
}
*/
