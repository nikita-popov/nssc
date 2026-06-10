package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [options]\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Commands: run, adduser")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runServer(os.Args[2:])
	case "adduser":
		addUser(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
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

	mux := http.NewServeMux()

	// Serve the embedded stylesheet so the browser does not get a 404.
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "max-age=3600")
		fmt.Fprint(w, frontend.CSS)
	})

	apiHandler := api.NewHandler(db, rootDir, ufss)
	mux.Handle("/api/", http.StripPrefix("/api", apiHandler))

	webdavHandler := webdav.NewHandler(db, rootDir, ufss)
	mux.Handle("/webdav/", webdavHandler)

	frontendHandler := frontend.NewHandler(db, rootDir, ufss, version, 0)
	mux.Handle("/", frontendHandler)

	if *ninepAddr != "" {
		srv9p := ninep.NewServer(db, ufss)
		go func() {
			log.Printf("9P server listening on %s", *ninepAddr)
			if err := srv9p.ListenAndServe(*ninepAddr); err != nil {
				log.Fatalf("9P server: %v", err)
			}
		}()
	}

	log.Printf("HTTP server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("HTTP server: %v", err)
	}
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

	db := &users.UsersDB{}
	dbPath := filepath.Join(rootDir, "db.json")
	_ = db.Load(dbPath)
	db.SetRoot(dbPath)

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Password: ")
	password, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("adduser: failed to read password: %v", err)
	}
	password = strings.TrimRight(password, "\r\n")

	fmt.Print("Confirm password: ")
	tmp, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("adduser: failed to read password confirmation: %v", err)
	}
	tmp = strings.TrimRight(tmp, "\r\n")

	if password != tmp {
		log.Fatal("adduser: passwords do not match")
	}

	if err := db.AddUser(username, password, quota); err != nil {
		log.Fatalf("adduser: %v", err)
	}

	userDir := filepath.Join(rootDir, "user", username)
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		log.Fatalf("adduser: failed to create user directory: %v", err)
	}

	if err := db.Save(dbPath); err != nil {
		log.Fatalf("adduser: failed to save database: %v", err)
	}

	log.Printf("adduser: user %q added successfully", username)
}
