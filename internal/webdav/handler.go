package webdav

import (
	"net/http"
	"strings"

	"golang.org/x/net/webdav"

	"nssc/internal/fs"
	"nssc/internal/users"
)

// WebDAVHandler serves WebDAV requests, routing per-user to the corresponding UserFS.
type WebDAVHandler struct {
	db      *users.UsersDB
	rootDir string
	fs      *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, ufs *fs.UserFSServer) http.Handler {
	return &WebDAVHandler{
		db:      db,
		rootDir: rootDir,
		fs:      ufs,
	}
}

func (h *WebDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/webdav/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	username := parts[0]

	user, pass, ok := r.BasicAuth()
	if !ok || user != username || !h.db.Authenticate(user, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ufs, err := h.fs.GetUserFS(user)
	if err != nil {
		http.Error(w, "User filesystem not found", http.StatusNotFound)
		return
	}

	// LockSystem is created per ServeHTTP call to prevent cross-user lock leakage.
	handler := &webdav.Handler{
		Prefix:     "/webdav/" + username,
		FileSystem: ufs,
		LockSystem: webdav.NewMemLS(),
	}

	h.quotaMiddleware(handler, ufs).ServeHTTP(w, r)
}

// quotaMiddleware rejects write operations that would exceed the user's quota.
// ufs is passed directly to avoid a redundant GetUserFS lookup.
func (h *WebDAVHandler) quotaMiddleware(next http.Handler, ufs *fs.UserFS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT", "MKCOL", "COPY", "MOVE":
			total, used, _ := ufs.GetQuota()
			if total > 0 {
				var needed int64
				if r.Method == "PUT" {
					needed = r.ContentLength
				}
				if uint64(used+needed) > uint64(total) {
					http.Error(w, "Quota exceeded", http.StatusInsufficientStorage)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
