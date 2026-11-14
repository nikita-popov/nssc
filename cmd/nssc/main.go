package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"nssc/internal/api"
	"nssc/internal/frontend"
	"nssc/internal/fs"
	"nssc/internal/ninep"
	"nssc/internal/users"
	"nssc/internal/webdav"
)

// version is set at build time via:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/nssc
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <command> [options]\n", os.Args[0])
	fmt.Fprintln(os.Stderr, "Commands: run, adduser, version")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "run":
		runServer(os.Args[2:])
	case "adduser":
		addUser(os.Args[2:])
	case "version", "-version", "--version", "-v":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		usage()
	}
}

// buildMux wires all protocol handlers onto one mux. It is separated from
// runServer so tests can exercise the real routing (including the
// http.StripPrefix behaviour that the old unit tests papered over).
func buildMux(db *users.UsersDB, rootDir string, ufss *fs.UserFSServer) http.Handler {
	mux := http.NewServeMux()

	// Serve the embedded stylesheet so the browser does not get a 404.
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=3600")
		fmt.Fprint(w, frontend.CSS)
	})

	// Public share links. Without this route /public/<uuid> fell through to
	// the frontend and every share link returned 401.
	mux.Handle("/public/", newPublicHandler(filepath.Join(rootDir, "public")))

	mux.Handle("/api/", http.StripPrefix("/api", api.NewHandler(db, rootDir, ufss)))
	mux.Handle("/webdav/", webdav.NewHandler(db, rootDir, ufss))
	mux.Handle("/", frontend.NewHandler(db, rootDir, ufss, version, 0))

	return mux
}

// newPublicHandler serves read-only public shares. Only symlinks created by
// ShareManager inside publicDir are honoured: no directory listing, no path
// traversal, no access to regular files dropped into the directory.
func newPublicHandler(publicDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/public/")
		if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
			http.NotFound(w, r)
			return
		}

		linkPath := filepath.Join(publicDir, id)
		info, err := os.Lstat(linkPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			http.NotFound(w, r)
			return
		}
		target, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		fi, err := os.Stat(target)
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		w.Header().Set("X-Content-Type-Options", "nosniff")
		if cd := mime.FormatMediaType("inline", map[string]string{"filename": fi.Name()}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
	})
}

func runServer(args []string) {
	flags := flag.NewFlagSet("run", flag.ExitOnError)
	addr := flags.String("p", ":0", "HTTP listen address")
	ninepAddr := flags.String("9p", "", "9P listen address (e.g. :564 or unix:///run/nssc.sock)")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}

	if flags.NArg() < 1 {
		log.Fatal("run: missing storage directory argument")
	}
	rootDir := flags.Arg(0)

	if info, err := os.Stat(rootDir); err != nil || !info.IsDir() {
		log.Fatalf("run: storage directory %q not found or not a directory", rootDir)
	}

	db := &users.UsersDB{}
	if err := db.Load(filepath.Join(rootDir, "db.json")); err != nil {
		log.Fatalf("run: failed to load users database: %v", err)
	}

	ufss, err := fs.NewUserFSServer(rootDir, nil, db.Users)
	if err != nil {
		log.Fatalf("run: failed to init user FS: %v", err)
	}

	mux := buildMux(db, rootDir, ufss)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sockPath string
	if *ninepAddr != "" {
		srv9p := ninep.NewServer(db, ufss)
		// Bind before the HTTP server starts: a 9P bind failure must abort
		// startup rather than leave the service "healthy" with 9P dead.
		ln, err := srv9p.Listen(*ninepAddr)
		if err != nil {
			log.Fatalf("9P listen on %s: %v", *ninepAddr, err)
		}
		if p, ok := ninep.UnixSocketPath(*ninepAddr); ok {
			sockPath = p
		}
		go func() {
			log.Printf("9P server listening on %s", *ninepAddr)
			if err := srv9p.Serve(ln); err != nil && ctx.Err() == nil {
				log.Printf("9P server: %v", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// ReadHeaderTimeout closes the slowloris hole; Read/Write timeouts
		// stay unset because uploads and downloads may legitimately run long.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		<-ctx.Done()
		log.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
	}()

	log.Printf("HTTP server listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server: %v", err)
	}
	if sockPath != "" {
		if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
			log.Printf("failed to remove 9P socket %s: %v", sockPath, err)
		}
	}
	log.Printf("shutdown complete")
}

func addUser(args []string) {
	if len(args) < 2 {
		log.Fatal("adduser: usage: adduser <dir> <username> [quota]")
	}

	rootDir := args[0]
	username := args[1]
	quota := "1GiB"
	if len(args) >= 3 {
		quota = args[2]
	}

	if !users.ValidName(username) {
		log.Fatalf("adduser: invalid username %q", username)
	}

	db := &users.UsersDB{}
	dbPath := filepath.Join(rootDir, "db.json")
	if err := db.Load(dbPath); err != nil {
		// Silently ignoring this used to overwrite an unparsable database
		// and drop every existing user.
		log.Fatalf("adduser: refusing to overwrite unreadable %s: %v", dbPath, err)
	}
	db.SetRoot(dbPath)

	password, confirm, err := readPasswordPair()
	if err != nil {
		log.Fatalf("adduser: failed to read password: %v", err)
	}
	if password != confirm {
		log.Fatal("adduser: passwords do not match")
	}
	if len(password) < 8 {
		log.Printf("adduser: warning: password is shorter than 8 characters")
	}

	if err := db.AddUser(username, password, quota); err != nil {
		log.Fatalf("adduser: %v", err)
	}

	userDir := fs.UserRoot(rootDir, username)
	if err := os.MkdirAll(userDir, 0700); err != nil {
		log.Fatalf("adduser: failed to create user directory: %v", err)
	}

	if err := db.Save(dbPath); err != nil {
		log.Fatalf("adduser: failed to save database: %v", err)
	}

	log.Printf("adduser: user %q added successfully", username)
}

// readPasswordPair reads the password twice. On a terminal echo is disabled;
// when stdin is a pipe ("printf 'pw\npw\n' | nssc adduser ...") two lines are
// read, which keeps scripted use working.
func readPasswordPair() (string, string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		first, err := promptPassword(fd, "Password: ")
		if err != nil {
			return "", "", err
		}
		second, err := promptPassword(fd, "Confirm password: ")
		if err != nil {
			return "", "", err
		}
		return first, second, nil
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return "", "", errors.New("expected two lines on stdin: password and confirmation")
	}
	return lines[0], lines[1], nil
}

func promptPassword(fd int, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
