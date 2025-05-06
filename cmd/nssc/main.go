package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	//"aqwari.net/net/styx"

	"nssc/internal/api"
	"nssc/internal/frontend"
	"nssc/internal/fs"
	//"nssc/internal/ninep"
	"nssc/internal/users"
	"nssc/internal/webdav"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage:")
		fmt.Println("  adduser <rootDir> <username> <quota>")
		fmt.Println("  run <host> <rootDir>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "adduser":
		if len(os.Args) != 5 {
			log.Fatal("Usage: adduser <rootDir> <username> <quota>")
		}
		rootDir := os.Args[2]
		username := os.Args[3]
		quota := os.Args[4]

		dbPath := filepath.Join(rootDir, "db.json")
		var db users.UsersDB
		if err := db.Load(dbPath); err != nil {
			log.Printf("Load DB error: %v, creating new DB", err)
			db = users.UsersDB{}
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Enter password: ")
		password, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal("Password reading error:", err)
		}
		fmt.Print("Repeat password: ")
		tmp, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal("Password reading error:", err)
		}
		if tmp != password {
			log.Fatal("Passwords mismatch")
		}
		if err := db.AddUser(username, password, quota); err != nil {
			log.Fatal("Add user error:", err)
		}
		if err := db.Save(dbPath); err != nil {
			log.Fatal("Save DB error:", err)
		}
		fmt.Printf("User %s added successfully\n", username)
	case "run":
		var (
			//styxServer styx.Server
			ufss *fs.UserFSServer
		)
		if len(os.Args) != 4 {
			log.Fatal("Usage: run <host> <rootDir>")
		}
		host := os.Args[2]
		rootDir := os.Args[3]
		dbPath := filepath.Join(rootDir, "db.json")
		var db users.UsersDB
		if err := db.Load(dbPath); err != nil {
			log.Fatalf("Failed to load users DB: %v", err)
		}
		log.Printf("Users loaded: %d", len(db.Users))
		db.SetRoot(rootDir)
		err := os.MkdirAll(filepath.Join(rootDir, "user"), 0755)
		if err != nil {
			log.Fatalf("Failed to create users dir: %v", err)
		}
		err = os.MkdirAll(filepath.Join(rootDir, "public"), 0755)
		if err != nil {
			log.Fatalf("Failed to create public dir: %v", err)
		}
		mainQuota := fs.NewQuota(0) // TODO
		ufss, _ = fs.NewUserFSServer(filepath.Join(rootDir, "user"), mainQuota, db.Users)

		/*go func() {
			srv := ninep.NewServer(&db, rootDir)
			styxServer.Addr = ":564"
			//styxServer.Auth = srv.authFunc()
			styxServer.Handler = styx.Stack(srv)
			styxServer.ListenAndServe()
		}()*/

		frontendHandler := frontend.NewHandler(&db, rootDir, ufss)
		frontendHandler.FillCSS()
		apiHandler := api.NewHandler(&db, rootDir, ufss)
		publicHandler := http.StripPrefix("/public/",
			http.FileServer(http.Dir(filepath.Join(rootDir, "public"))))
		webdavHandler := webdav.NewHandler(&db, rootDir, ufss)

		mux := http.NewServeMux()
		mux.Handle("/", frontendHandler)
		mux.HandleFunc("/style.css", frontendHandler.ServeCSSFile)
		mux.HandleFunc("/favicon.ico", frontendHandler.ServeFaviconFile)
		mux.Handle("/api/", http.StripPrefix("/api", apiHandler))
		mux.Handle("/public/", publicHandler)
		mux.Handle("/webdav/", webdavHandler)

		log.Printf("Server running at %s", host)
		log.Fatal(http.ListenAndServe(host, mux))
	default:
		log.Fatal("Unknown command")
	}
}
