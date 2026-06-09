package frontend

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/golang-jwt/jwt/v5"

	"nssc/internal/fs"
	"nssc/internal/share"
	"nssc/internal/users"
)

const (
	defaultUploadMaxMemory = 100 << 20 // 100 MiB
	sessionCookieName      = "nssc_session"
)

type FrontendHandler struct {
	db              *users.UsersDB
	rootDir         string
	shareMgr        *share.ShareManager
	template        *template.Template
	fs              *fs.UserFSServer
	uploadMaxMemory int64 // max multipart memory; 0 → defaultUploadMaxMemory
}

// NewHandler creates a FrontendHandler.
// Pass maxMemory > 0 to override the default 100 MiB multipart limit.
func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer, maxMemory int64) *FrontendHandler {
	if maxMemory <= 0 {
		maxMemory = defaultUploadMaxMemory
	}
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &FrontendHandler{
		db:              db,
		rootDir:         rootDir,
		shareMgr:        shareMgr,
		template:        tplPage,
		fs:              fs,
		uploadMaxMemory: maxMemory,
	}
}

// GetUserFromCookie looks up the authenticated user from the nssc_session cookie.
// The JWT "sub" claim carries the username, so only one bcrypt verification is
// performed regardless of how many users exist in the database.
func (h *FrontendHandler) GetUserFromCookie(r *http.Request) *users.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	// Parse without verification to extract the "sub" claim cheaply.
	// Full signature verification follows with the user's own key.
	unverified, _, err := new(jwt.Parser).ParseUnverified(cookie.Value, jwt.MapClaims{})
	if err != nil {
		return nil
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return nil
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil
	}
	u := h.db.GetUser(sub)
	if u == nil {
		return nil
	}
	token, err := u.ValidateJWT(cookie.Value)
	if err != nil || !token.Valid {
		return nil
	}
	return u
}

func (h *FrontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	username, pass, ok := r.BasicAuth()
	if !ok || !h.db.Authenticate(username, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user = h.db.GetUser(username)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
		Name:     sessionCookieName,
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
		http.Redirect(w, r, "/user/", http.StatusSeeOther)
		return
	}
	// Only paths under /user/ are handled by userHandler.
	// Anything else (/style.css, /favicon.ico, …) is a 404.
	if !strings.HasPrefix(r.URL.Path, "/user/") && r.URL.Path != "/user" {
		http.NotFound(w, r)
		return
	}
	h.userHandler(w, r, username, ufs)
}

func (h *FrontendHandler) userHandler(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	ctx := context.Background()
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
	if err := r.ParseMultipartForm(h.uploadMaxMemory); err != nil {
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

	sz := header.Size
	var src io.Reader = file
	if sz < 0 {
		// Unknown size (streaming upload): buffer to get the real size.
		buf := &bytes.Buffer{}
		if _, err := io.Copy(buf, file); err != nil {
			http.Error(w, "Read error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sz = int64(buf.Len())
		src = buf
	}

	dstPath := filepath.Join(curPath, header.Filename)
	if err := ufs.WriteFile(dstPath, src, sz); err != nil {
		log.Printf("File saving error: %v", err)
		http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Form file %s saved to %s", header.Filename, dstPath)
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

func (h *FrontendHandler) handleMkdir(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	ctx := context.Background()
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
	fullPath := filepath.Join(curPath, dirname)
	if err := ufs.Mkdir(ctx, fullPath, 0755); err != nil {
		log.Printf("Mkdir error: %v", err)
		http.Error(w, "Create error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("Directory %s created in %s", dirname, fullPath)
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

func (h *FrontendHandler) handleDelete(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	ctx := context.Background()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	paths := r.Form["path"]
	curPath := r.FormValue("dir")
	for _, p := range paths {
		path := filepath.Join(curPath, p)
		if err := ufs.RemoveAll(ctx, path); err != nil {
			http.Error(w, "Delete error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("File %s removed", path)
	}
	http.Redirect(w, r, "/user/"+curPath, http.StatusSeeOther)
}

func (h *FrontendHandler) handleLogout(w http.ResponseWriter, r *http.Request, user string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	log.Printf("User %s logged out", user)
	// 303 See Other: browser follows redirect with GET regardless of original method.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *FrontendHandler) handleSearch(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	query := r.FormValue("q")
	if len(query) > 200 {
		http.Error(w, "Search query too long", http.StatusBadRequest)
		return
	}
	re, err := regexp.Compile(query)
	if err != nil {
		http.Error(w, "Invalid search pattern: "+err.Error(), http.StatusBadRequest)
		return
	}
	results, _ := ufs.Search(re)
	quotaTotal, quotaUsed, _ := ufs.GetQuota()
	data := PageData{
		User:          user,
		Files:         results,
		SearchQuery:   query,
		QuotaTotal:    uint64(quotaTotal),
		QuotaTotalStr: humanize.IBytes(uint64(quotaTotal)),
		QuotaUsed:     uint64(quotaUsed),
		QuotaUsedStr:  humanize.IBytes(uint64(quotaUsed)),
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

func (h *FrontendHandler) handleShare(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	relPath := r.FormValue("path")
	// Validate that the path exists inside the user FS (resolvePath guards traversal).
	if _, err := ufs.Stat(context.Background(), relPath); err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	userRoot := filepath.Join(h.rootDir, "user", user)
	link, err := h.shareMgr.CreateShare(userRoot, relPath)
	if err != nil {
		log.Printf("Share error: %v", err)
		http.Error(w, "Sharing failed", http.StatusInternalServerError)
		return
	}
	curPath := filepath.Dir(relPath)
	http.Redirect(w, r, "/user/"+curPath+"?shared="+link, http.StatusSeeOther)
}

// suppress unused import errors when errors/template are only used in other files
var _ = errors.New
var _ = template.HTMLEscapeString
