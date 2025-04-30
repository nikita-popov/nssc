package webdav

import (
	"net/http"
	"strings"

	"golang.org/x/net/webdav"

	"nssc/internal/fs"
	"nssc/internal/users"
)

type WebDAVHandler struct {
	db         *users.UsersDB
	rootDir    string
	fileSystem webdav.FileSystem
	lockSystem webdav.LockSystem
	fs         *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer) http.Handler {
	return &WebDAVHandler{
		db:         db,
		rootDir:    rootDir,
		fileSystem: webdav.Dir(rootDir),
		lockSystem: webdav.NewMemLS(),
		fs:         fs,
	}
}

func (h *WebDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/webdav/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 {
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
		// TODO
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	handler := &webdav.Handler{
		Prefix:     "/webdav/" + username,
		FileSystem: ufs,
		LockSystem: h.lockSystem,
	}

	h.quotaMiddleware(handler).ServeHTTP(w, r)
}

// Middleware for quota checking
func (h *WebDAVHandler) quotaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" || r.Method == "MKCOL" || r.Method == "COPY" || r.Method == "MOVE" {
			username := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/webdav/"), "/", 2)[0]
			user := h.db.GetUser(username)

			ufs, err := h.fs.GetUserFS(user.Name)
			if err != nil {
				// TODO
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

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
