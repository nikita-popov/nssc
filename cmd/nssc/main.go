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
	"nssc/internal/fs"
	"nssc/internal/frontend"
	"nssc/internal/ninep"
	"nssc/internal/users"
	"nssc/internal/webdav"
)

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

	db := users.NewUsersDB()
	if err := db.Load(filepath.Join(rootDir, "db.json")); err != nil {
		log.Fatalf("run: failed to load users database: %v", err)
	}

	ufss, err := fs.NewUserFSServer(db, rootDir)
	if err != nil {
		log.Fatalf("run: failed to init user FS: %v", err)
	}

	mux := http.NewServeMux()

	apiHandler := api.NewAPIHandler(db, ufss)
	mux.Handle("/api/", http.StripPrefix("/api", apiHandler))

	webdavHandler := webdav.NewWebDAVHandler(db, ufss)
	mux.Handle("/webdav/", webdavHandler)

	frontendHandler := frontend.NewFrontendHandler(db, ufss, rootDir)
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

	db := users.NewUsersDB()
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

	log.Printf("adduser: user %q added successfully", username)
}
