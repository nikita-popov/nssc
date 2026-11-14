package frontend

import (
	"context"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
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
	defaultUploadMaxMemory = 8 << 20 // 8 MiB of multipart kept in RAM, rest spills to disk
	sessionCookieName      = "nssc_session"
)

type FrontendHandler struct {
	db              *users.UsersDB
	rootDir         string
	version         string
	shareMgr        *share.ShareManager
	template        *template.Template
	fs              *fs.UserFSServer
	uploadMaxMemory int64 // max multipart memory; 0 → defaultUploadMaxMemory
}

// NewHandler creates a FrontendHandler.
// version is the build-time version string (set via -ldflags "-X main.version=...").
// Pass maxMemory > 0 to override the default multipart memory limit.
func NewHandler(db *users.UsersDB, rootDir string, ufss *fs.UserFSServer, version string, maxMemory int64) *FrontendHandler {
	if maxMemory <= 0 {
		maxMemory = defaultUploadMaxMemory
	}
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &FrontendHandler{
		db:              db,
		rootDir:         rootDir,
		version:         version,
		shareMgr:        shareMgr,
		template:        tplPage,
		fs:              ufss,
		uploadMaxMemory: maxMemory,
	}
}

// secureRequest reports whether the session cookie may be marked Secure.
// Hard-coding Secure: true breaks login behind a plain-HTTP reverse proxy,
// where the browser silently drops the cookie and the user loops on the
// login prompt.
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// GetUserFromCookie looks up the authenticated user from the nssc_session cookie.
func (h *FrontendHandler) GetUserFromCookie(r *http.Request) *users.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	// Parse without verification to extract the "sub" claim cheaply;
	// full signature verification follows with the user's own key.
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
		Secure:   secureRequest(r),
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
		defer f.Close()
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "File serving not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		disposition := "attachment"
		if r.URL.Query().Get("preview") != "" {
			disposition = "inline"
		}
		if cd := mime.FormatMediaType(disposition, map[string]string{"filename": fi.Name()}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
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
		rel := filepath.ToSlash(filepath.Join(curPath, f.Name()))
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
	if curPath != "" && curPath != "/" {
		parentPath = filepath.ToSlash(filepath.Dir(curPath))
		if parentPath == "." {
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
		FilesCount:    filesCount,
		DirsCount:     dirsCount,
		Version:       h.version,
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

func (h *FrontendHandler) handleUpload(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	if err := r.ParseMultipartForm(h.uploadMaxMemory); err != nil {
		log.Printf("Form parse error: %v", err)
		http.Error(w, "Malformed upload", http.StatusBadRequest)
		return
	}
	curPath := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Form file error: %v", err)
		http.Error(w, "Malformed upload", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// header.Size is -1 for streaming uploads; WriteFile charges the bytes
	// that actually arrive, so there is no need to buffer the body in RAM
	// just to learn its length.
	dstPath := filepath.Join(curPath, header.Filename)
	if err := ufs.WriteFile(dstPath, file, header.Size); err != nil {
		log.Printf("File saving error: %v", err)
		if strings.Contains(err.Error(), "quota exceeded") {
			http.Error(w, "Quota exceeded", http.StatusInsufficientStorage)
			return
		}
		http.Error(w, "Save failed", http.StatusInternalServerError)
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
		http.Error(w, "Directory name required", http.StatusBadRequest)
		return
	}
	fullPath := filepath.Join(curPath, dirname)
	if err := ufs.Mkdir(ctx, fullPath, 0755); err != nil {
		log.Printf("Mkdir error: %v", err)
		http.Error(w, "Create failed", http.StatusInternalServerError)
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
		target := filepath.Join(curPath, p)
		if err := ufs.RemoveAll(ctx, target); err != nil {
			log.Printf("Delete error for %s: %v", target, err)
			http.Error(w, "Delete failed", http.StatusInternalServerError)
			return
		}
		log.Printf("File %s removed", target)
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
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
	log.Printf("User %s logged out", user)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *FrontendHandler) handleSearch(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	// The template used to submit "query" while the handler read "q", so the
	// pattern was empty and every file matched. Accept both spellings.
	query := strings.TrimSpace(r.FormValue("q"))
	if query == "" {
		query = strings.TrimSpace(r.FormValue("query"))
	}
	if len(query) > 200 {
		http.Error(w, "Search query too long", http.StatusBadRequest)
		return
	}

	results := []fs.FileEntry{}
	if query != "" {
		// Treat the input as a literal substring: the box is labelled "Search
		// term", and a raw user regexp is a free ReDoS/DoS vector.
		re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
		if err != nil {
			http.Error(w, "Invalid search term", http.StatusBadRequest)
			return
		}
		results, _ = ufs.Search(re)
	}
	quotaTotal, quotaUsed, _ := ufs.GetQuota()
	data := PageData{
		User:          user,
		Files:         results,
		SearchQuery:   query,
		FilesCount:    len(results),
		QuotaTotal:    uint64(quotaTotal),
		QuotaTotalStr: humanize.IBytes(uint64(quotaTotal)),
		QuotaUsed:     uint64(quotaUsed),
		QuotaUsedStr:  humanize.IBytes(uint64(quotaUsed)),
		Version:       h.version,
	}
	if err := h.template.Execute(w, data); err != nil {
		log.Printf("Template execute error: %v", err)
	}
}

// shareRelPath accepts the current "path" field and the legacy "name" field
// (which posted "/{user}/{rel}") and returns a path relative to the user root.
func (h *FrontendHandler) shareRelPath(ctx context.Context, ufs *fs.UserFS, user, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", os.ErrNotExist
	}
	trimmed := strings.TrimPrefix(raw, "/")
	candidates := []string{trimmed}
	if strings.HasPrefix(trimmed, user+"/") {
		candidates = append(candidates, strings.TrimPrefix(trimmed, user+"/"))
	}
	candidates = append(candidates, raw)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := ufs.Stat(ctx, c); err == nil {
			return c, nil
		}
	}
	return "", os.ErrNotExist
}

func (h *FrontendHandler) handleShare(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}
	ctx := context.Background()
	raw := r.FormValue("path")
	if raw == "" {
		raw = r.FormValue("name")
	}
	relPath, err := h.shareRelPath(ctx, ufs, user, raw)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	// Share from the filesystem root that actually backs this user; deriving
	// <root>/user/<name> here broke every instance whose data lived elsewhere.
	link, err := h.shareMgr.CreateShare(ufs.Root(), relPath)
	if err != nil {
		log.Printf("Share error: %v", err)
		http.Error(w, "Sharing failed", http.StatusInternalServerError)
		return
	}
	curPath := filepath.ToSlash(filepath.Dir(relPath))
	if curPath == "." {
		curPath = ""
	}
	http.Redirect(w, r, "/user/"+curPath+"?shared="+link, http.StatusSeeOther)
}
