package webdav

import (
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/webdav"

	"nssc/internal/fs"
	"nssc/internal/users"
)

// WebDAVHandler serves WebDAV requests, routing per-user to the corresponding UserFS.
type WebDAVHandler struct {
	db      *users.UsersDB
	rootDir string
	fs      *fs.UserFSServer

	mu    sync.Mutex
	locks map[string]webdav.LockSystem
}

func NewHandler(db *users.UsersDB, rootDir string, ufs *fs.UserFSServer) http.Handler {
	return &WebDAVHandler{
		db:      db,
		rootDir: rootDir,
		fs:      ufs,
		locks:   make(map[string]webdav.LockSystem),
	}
}

// lockSystem returns the per-user lock system. It is kept across requests:
// a fresh MemLS per request (the previous behaviour) meant locks were never
// visible to the request that had to honour them.
func (h *WebDAVHandler) lockSystem(user string) webdav.LockSystem {
	h.mu.Lock()
	defer h.mu.Unlock()
	ls, ok := h.locks[user]
	if !ok {
		ls = webdav.NewMemLS()
		h.locks[user] = ls
	}
	return ls
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

	handler := &webdav.Handler{
		Prefix:     "/webdav/" + username,
		FileSystem: ufs,
		LockSystem: h.lockSystem(username),
	}

	h.quotaMiddleware(handler, ufs).ServeHTTP(w, r)
}

// quotaMiddleware rejects writes that are known up front to exceed the quota.
// Uploads of unknown length (Transfer-Encoding: chunked, ContentLength == -1)
// are let through: UserFS.quotaFile enforces the quota per Write call, which
// is the only correct way to police a stream whose size is not declared.
func (h *WebDAVHandler) quotaMiddleware(next http.Handler, ufs *fs.UserFS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT", "MKCOL", "COPY", "MOVE":
			total, used, _ := ufs.GetQuota()
			if total > 0 && r.Method == "PUT" && r.ContentLength > 0 {
				if r.ContentLength > total-used {
					http.Error(w, "Quota exceeded", http.StatusInsufficientStorage)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
