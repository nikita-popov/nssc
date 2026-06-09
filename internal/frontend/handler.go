package frontend

import (
	"context"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dustin/go-humanize"

	"nssc/internal/fs"
	"nssc/internal/share"
	"nssc/internal/users"
)

const maxSearchQueryLen = 200

type FrontendHandler struct {
	db       *users.UsersDB
	rootDir  string
	shareMgr *share.ShareManager
	template *template.Template
	fs       *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer) *FrontendHandler {
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &FrontendHandler{db: db, rootDir: rootDir, shareMgr: shareMgr, template: tplPage, fs: fs}
}

// GetUserFromCookie reads the username from the JWT "sub" claim (O(1) lookup),
// then validates the token against that user's key.
func (h *FrontendHandler) GetUserFromCookie(r *http.Request) *users.User {
	for _, cookie := range r.Cookies() {
		// Extract username from JWT sub claim without verifying signature yet.
		username, err := users.GetUsernameFromJWT(cookie.Value)
		if err != nil {
			continue
		}
		// Find the user by the extracted name (O(1) in practice for small user sets).
		user := h.db.GetUser(username)
		if user == nil {
			continue
		}
		// Now verify the signature with the user's actual key.
		token, err := user.ValidateJWT(cookie.Value)
		if err != nil || !token.Valid {
			continue
		}
		return user
	}
	return nil
}

func (h *FrontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	if cookies := r.Cookies(); len(cookies) > 0 {
		user := h.GetUserFromCookie(r)
		if user != nil {
			ufs, err := h.fs.GetUserFS(user.Name)
			if err != nil {
				log.Printf("User FS error: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			h.handleAuthorizedRequest(w, r, ctx, user.Name, ufs)
			return
		}
	}
	username, pass, ok := r.BasicAuth()
	if !ok || !h.db.Authenticate(username, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user := h.db.GetUser(username)
	if user == nil {
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}
	ufs, err := h.fs.GetUserFS(user.Name)
	if err != nil {
		log.Printf("User FS error: %s", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	token, err := user.GenerateJWT()
	if err != nil {
		log.Printf("User JWT error: %s", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     user.Name,
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	log.Printf("User %s logged in", username)
	h.handleAuthorizedRequest(w, r, ctx, username, ufs)
}

func (h *FrontendHandler) handleAuthorizedRequest(w http.ResponseWriter, r *http.Request, ctx context.Context, username string, ufs *fs.UserFS) {
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/logout":
			h.handleLogout(w, r, username)
			return
		case "/mkdir":
			h.handleMkdir(w, r, ctx, username, ufs)
			return
		case "/rm":
			h.handleDelete(w, r, ctx, username, ufs)
			return
		case "/search":
			h.handleSearch(w, r, ctx, username, ufs)
			return
		case "/share":
			h.handleShare(w, r, ctx, username, ufs)
			return
		case "/upload":
			h.handleUpload(w, r, ctx, username, ufs)
			return
		}
	}
	if r.URL.Path == "/" {
		h.rootHandler(w, r)
		return
	}
	h.userHandler(w, r, ctx, username, ufs)
}

func (h *FrontendHandler) rootHandler(w http.ResponseWriter, r *http.Request) {
	username, _, ok := r.BasicAuth()
	if !ok || username == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/user/", http.StatusSeeOther)
}

func (h *FrontendHandler) userHandler(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	pathPrefix := "/user"
	relPath := strings.TrimPrefix(r.URL.Path, pathPrefix)
	decodedPath, err := url.PathUnescape(relPath)
	if err != nil {
		http.Error(w, "Invalid path encoding", http.StatusBadRequest)
		return
	}
	curPath := filepath.Clean(decodedPath)
	fi, err := ufs.Stat(ctx, decodedPath)
	if err != nil {
		log.Printf("Path %s stat error: %v", decodedPath, err)
		http.Error(w, "Forbidden path", http.StatusForbidden)
		return
	}
	if !fi.IsDir() {
		f, err := ufs.Open(ctx, decodedPath)
		if err != nil {
			log.Printf("Path %s open error: %v", decodedPath, err)
			http.Error(w, "Forbidden path", http.StatusForbidden)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "File serving not supported", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
		return
	}
	files, err := ufs.ReadDir(decodedPath)
	if err != nil {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
	}
	var (
		fileEntries []fs.FileEntry
		filesCount  int
		dirsCount   int
	)
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			log.Printf("File info error: %v", err)
			continue
		}
		size := ""
		if !info.IsDir() {
			size = humanize.IBytes(uint64(info.Size()))
		}
		modTime := info.ModTime().Format("2006-01-02T15:04:05+0000")
		rel := filepath.Join(curPath, f.Name())
		if info.IsDir() {
			dirsCount++
		} else {
			filesCount++
		}
		fileEntries = append(fileEntries, fs.FileEntry{
			Name:    f.Name(),
			RelPath: rel,
			IsDir:   info.IsDir(),
			Size:    size,
			ModTime: modTime,
		})
	}
	parentPath := ""
	if curPath != "" {
		parentPath = filepath.Dir(curPath)
		if curPath == "/" {
			parentPath = ""
		}
	}
	quotaTotal, quotaUsed, _ := ufs.GetQuota()
	data := PageData{
		User:          user,
		CurrentPath:   curPath,
		ParentPath:    parentPath,
		Files:         fileEntries,
		QuotaTotal:    uint64(quotaTotal),
		QuotaTotalStr: humanize.IBytes(uint64(quotaTotal)),
		QuotaUsed:     uint64(quotaUsed),
		QuotaUsedStr:  humanize.IBytes(uint64(quotaUsed)),
		SearchQuery:   "",
		FilesCount:    filesCount,
		DirsCount:     dirsCount,
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

func (h *FrontendHandler) handleUpload(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		log.Printf("Form parse error: %v", err)
		http.Error(w, "Form parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	curPath := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Form file error: %v", err)
		http.Error(w, "File error: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	dstPath := filepath.Join(curPath, header.Filename)
	if err = ufs.WriteFile(dstPath, file, header.Size); err != nil {
		log.Printf("File saving error: %v", err)
		http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Form file %s saved to %s", header.Filename, dstPath)
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

func (h *FrontendHandler) handleMkdir(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	curPath := r.FormValue("path")
	dirname := r.FormValue("dirname")
	if dirname == "" {
		log.Printf("Malformed form: Directory name required")
		http.Error(w, "Directory name required", http.StatusBadRequest)
		return
	}
	if err := ufs.Mkdir(ctx, filepath.Join(curPath, dirname), 0755); err != nil {
		log.Printf("Mkdir error: %v", err)
		http.Error(w, "Create error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

func (h *FrontendHandler) handleDelete(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	paths := r.Form["path"]
	curPath := r.FormValue("dir")
	for _, p := range paths {
		if err := ufs.RemoveAll(ctx, filepath.Join(curPath, p)); err != nil {
			http.Error(w, "Delete error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

// handleLogout clears the session cookie and redirects to login (303).
func (h *FrontendHandler) handleLogout(w http.ResponseWriter, r *http.Request, username string) {
	http.SetCookie(w, &http.Cookie{
		Name:     username,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
	log.Printf("User %s logged out", username)
	// 303 SeeOther is correct for a POST → GET redirect after logout.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleSearch compiles a regex from the query and walks the user's tree.
// Query length is capped to prevent ReDoS.
func (h *FrontendHandler) handleSearch(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	query := r.FormValue("query")
	if query == "" {
		http.Redirect(w, r, "/user/", http.StatusSeeOther)
		return
	}
	if len(query) > maxSearchQueryLen {
		http.Error(w, "Search query too long", http.StatusBadRequest)
		return
	}
	log.Printf("Search for %s", query)
	re, err := regexp.Compile(query)
	if err != nil {
		http.Error(w, "Invalid regex pattern", http.StatusBadRequest)
		return
	}
	results, err := ufs.Search(re)
	if err != nil {
		http.Error(w, "Search failed", http.StatusInternalServerError)
		return
	}
	data := PageData{
		User:        user,
		CurrentPath: "search",
		ParentPath:  ".",
		Files:       results,
		SearchQuery: query,
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

// handleShare creates a public symlink for a file validated through UserFS.
func (h *FrontendHandler) handleShare(w http.ResponseWriter, r *http.Request, ctx context.Context, user string, ufs *fs.UserFS) {
	path := r.FormValue("name")
	// Validate path is inside user's sandbox before sharing.
	if _, err := ufs.Stat(ctx, path); err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	absPath := filepath.Join(ufs.Root(), filepath.Clean("/"+path))
	linkID, err := h.shareMgr.CreateShare(absPath)
	if err != nil {
		http.Error(w, "Share error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Share for %s created at %s", path, linkID)
	redirectURL := filepath.Dir("/user/" + path)
	http.Redirect(w, r, redirectURL+"?share="+linkID, http.StatusSeeOther)
}

func (h *FrontendHandler) FillCSS() {
	path := h.rootDir + "/style.css"
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		log.Println("CSS creating failed:", err)
		return
	}
	defer f.Close()
	if _, err = f.Write([]byte(CSS)); err != nil {
		log.Print("CSS filling failed:", err)
	}
}

func (h *FrontendHandler) ServeCSSFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := h.rootDir + "/style.css"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (h *FrontendHandler) ServeFaviconFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path := h.rootDir + "/favicon.ico"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
