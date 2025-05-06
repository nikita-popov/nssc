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

type FrontendHandler struct {
	ctx      context.Context
	db       *users.UsersDB
	rootDir  string // TODO: remove
	shareMgr *share.ShareManager
	template *template.Template
	fs       *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer) *FrontendHandler {
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &FrontendHandler{db: db, rootDir: rootDir, shareMgr: shareMgr, template: tplPage, fs: fs}
}

func (h *FrontendHandler) GetUserFromCookie(r *http.Request) *users.User {
	var user *users.User
	for key, _ := range h.db.Users {
		cookie, err := r.Cookie(h.db.Users[key].Name)
		if err != nil {
			continue
		}
		user = &h.db.Users[key]
		token, err := user.ValidateJWT(cookie.Value)
		if err != nil || !token.Valid {
			continue
		}
		break
	}
	return user
}

func (h *FrontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cookies := r.Cookies()
	if cookies != nil {
		user := h.GetUserFromCookie(r)
		if user != nil {
			ufs, err := h.fs.GetUserFS(user.Name)
			if err != nil {
				log.Printf("User FS error: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			h.handleAuthorizedRequest(w, r, user.Name, ufs)
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
	h.handleAuthorizedRequest(w, r, username, ufs)
}

func (h *FrontendHandler) handleAuthorizedRequest(w http.ResponseWriter, r *http.Request, username string, ufs *fs.UserFS) {
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/logout":
			h.handleLogout(w, r, username)
			return
		case "/mkdir":
			h.handleMkdir(w, r, username, ufs)
			return
		case "/rm":
			h.handleDelete(w, r, username, ufs)
			return
		case "/search":
			h.handleSearch(w, r, username, ufs)
			return
		case "/share":
			h.handleShare(w, r, username, ufs)
			return
		case "/upload":
			h.handleUpload(w, r, username, ufs)
			return
		}
	}
	if r.URL.Path == "/" {
		h.rootHandler(w, r)
		return
	}
	h.userHandler(w, r, username, ufs)
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

func (h *FrontendHandler) userHandler(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	pathPrefix := "/user"
	relPath := strings.TrimPrefix(r.URL.Path, pathPrefix)
	decodedPath, err := url.PathUnescape(relPath)
	if err != nil {
		http.Error(w, "Invalid path encoding", http.StatusBadRequest)
		return
	}
	curPath := filepath.Clean(decodedPath)
	fi, err := ufs.Stat(h.ctx, decodedPath)
	if err != nil {
		log.Printf("Path %s stat error: %v", decodedPath, err)
		http.Error(w, "Forbidden path", http.StatusForbidden)
		return
	}
	if !fi.IsDir() {
		f, err := ufs.Open(h.ctx, decodedPath)
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
	searchQuery := ""
	quotaTotal, quotaUsed, _ := ufs.GetQuota()
	quotaTotalStr := humanize.IBytes(uint64(quotaTotal))
	quotaUsedStr := humanize.IBytes(uint64(quotaUsed))
	data := PageData{
		User:          user,
		CurrentPath:   curPath,
		ParentPath:    parentPath,
		Files:         fileEntries,
		QuotaTotal:    uint64(quotaTotal),
		QuotaTotalStr: quotaTotalStr,
		QuotaUsed:     uint64(quotaUsed),
		QuotaUsedStr:  quotaUsedStr,
		SearchQuery:   searchQuery,
		FilesCount:    filesCount,
		DirsCount:     dirsCount,
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

func (h *FrontendHandler) handleUpload(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	// TODO: Size
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
	err = ufs.WriteFile(dstPath, file, header.Size)
	if err != nil {
		log.Printf("File saving error: %v", err)
		http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Form file %s saved to %s", header.Filename, dstPath)
	redirectURL := "/user/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *FrontendHandler) handleMkdir(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	err := r.ParseForm()
	if err != nil {
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
	fullPath := filepath.Join(curPath, dirname)
	err = ufs.Mkdir(h.ctx, fullPath, 0755)
	if err != nil {
		log.Printf("Mkdir error: %v", err)
		http.Error(w, "Create error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Directory %s created in %s", dirname, fullPath)
	redirectURL := "/user/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *FrontendHandler) handleDelete(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	paths := r.Form["path"]
	curPath := r.FormValue("dir")
	for _, p := range paths {
		path := filepath.Join(curPath, p)
		if err := ufs.RemoveAll(h.ctx, path); err != nil {
			http.Error(w, "Delete error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("File %s removed", path)
	}
	redirectURL := "/user/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

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
	http.Redirect(w, r, "/", http.StatusUnauthorized)
}

func (h *FrontendHandler) handleSearch(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	query := r.FormValue("query")
	log.Printf("Search for %s", query)
	if query == "" {
		http.Redirect(w, r, "/"+user, http.StatusSeeOther)
		return
	}
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

func (h *FrontendHandler) handleShare(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	// TODO: Rework with FS
	path := r.FormValue("name")
	userRoot := filepath.Join(h.rootDir, "user", user)
	absPath := filepath.Join(userRoot, strings.TrimPrefix(path, "/"+user+"/")) // TODO: to ufs
	linkID, err := h.shareMgr.CreateShare(absPath)
	if err != nil {
		http.Error(w, "Share error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Share for %s created at %s", path, linkID)
	redirectURL := filepath.Dir(path)
	http.Redirect(w, r, redirectURL+"?share="+linkID, http.StatusSeeOther)
}

// Check if CSS file exists, create if not
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
	_, err = f.Write([]byte(CSS))
	if err != nil {
		log.Print("CSS filling failed:", err)
		return
	}
}

func (h *FrontendHandler) ServeCSSFile(w http.ResponseWriter, r *http.Request) {
	path := h.rootDir + "/style.css"
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (h *FrontendHandler) ServeFaviconFile(w http.ResponseWriter, r *http.Request) {
	path := h.rootDir + "/favicon.ico"
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
