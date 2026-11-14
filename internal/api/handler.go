package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nssc/internal/fs"
	"nssc/internal/share"
	"nssc/internal/users"
)

type APIHandler struct {
	db       *users.UsersDB
	rootDir  string
	shareMgr *share.ShareManager
	fs       *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, ufss *fs.UserFSServer) *APIHandler {
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &APIHandler{
		db:       db,
		rootDir:  rootDir,
		shareMgr: shareMgr,
		fs:       ufss,
	}
}

func (h *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// main.go mounts this handler with http.StripPrefix("/api"), which leaves a
	// leading "/" on the path. Older code split the raw path and therefore
	// always produced an empty username, rejecting every request with 401.
	fullPath := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(fullPath, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		sendJSONError(w, "Invalid request path", http.StatusBadRequest)
		return
	}

	name := parts[0]
	path := ""
	if len(parts) > 1 {
		if unescaped, err := url.PathUnescape(parts[1]); err == nil {
			path = unescaped
		} else {
			path = parts[1]
		}
	}

	// Operations may be expressed as documented path suffixes:
	//   POST /api/{user}/{path}/share   -> create share link
	//   POST /api/{user}/{path}/        -> create directory
	isShare := false
	if strings.HasSuffix(path, "/share") {
		isShare = true
		path = strings.TrimSuffix(path, "/share")
	}
	isMkdir := strings.HasSuffix(path, "/")
	path = strings.TrimSuffix(path, "/")

	username, pass, ok := r.BasicAuth()
	if !ok || username != name || !h.db.Authenticate(username, pass) {
		log.Printf("User %s unauthorized", username)
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		sendJSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user := h.db.GetUser(username)
	if user == nil {
		sendJSONError(w, "User not found", http.StatusUnauthorized)
		return
	}
	ufs, err := h.fs.GetUserFS(user.Name)
	if err != nil {
		log.Printf("User FS error: %s", err)
		sendJSONError(w, "FS error", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, ctx, path, ufs)
	case http.MethodPost:
		h.handlePost(w, r, ctx, path, isShare, isMkdir, ufs)
	case http.MethodPut:
		h.handlePut(w, r, ctx, path, ufs)
	case http.MethodDelete:
		h.handleDelete(w, r, ctx, path, ufs)
	default:
		sendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleGet(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	info, err := ufs.Stat(ctx, path)
	if err != nil {
		sendJSONError(w, "Resource not found", http.StatusNotFound)
		return
	}

	if info.IsDir() {
		h.listDirectory(w, path, ufs)
		return
	}

	f, err := ufs.Open(ctx, path)
	if err != nil {
		sendJSONError(w, "Cannot open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// User-supplied content must never be sniffed into an active type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if t := mime.TypeByExtension(filepath.Ext(info.Name())); t != "" {
		w.Header().Set("Content-Type", t)
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	}))
}

func (h *APIHandler) listDirectory(w http.ResponseWriter, path string, ufs *fs.UserFS) {
	entries, err := ufs.ReadDir(path)
	if err != nil {
		sendJSONError(w, "Failed to read directory", http.StatusInternalServerError)
		return
	}

	response := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		response = append(response, map[string]interface{}{
			"name":      entry.Name(),
			"size":      info.Size(),
			"is_dir":    entry.IsDir(),
			"modified":  info.ModTime().Format(time.RFC3339),
			"mime_type": getMimeType(info),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("listDirectory encode error: %v", err)
	}
}

func (h *APIHandler) handlePost(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, isShare, isMkdir bool, ufs *fs.UserFS) {
	// Query parameters are kept for backwards compatibility with earlier
	// releases; the path suffixes are what README documents.
	if isShare || r.URL.Query().Get("share") != "" {
		h.createShare(w, ctx, path, ufs)
		return
	}
	if isMkdir || r.URL.Query().Get("mkdir") != "" {
		h.createDirectory(w, ctx, path, ufs)
		return
	}

	sendJSONError(w, "Invalid operation", http.StatusBadRequest)
}

func (h *APIHandler) createDirectory(w http.ResponseWriter, ctx context.Context, path string, ufs *fs.UserFS) {
	if path == "" {
		sendJSONError(w, "Directory name required", http.StatusBadRequest)
		return
	}
	if err := ufs.MkdirAll(ctx, path, 0750); err != nil {
		sendJSONError(w, "Directory creation failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "created",
		"path":   path,
	}); err != nil {
		log.Printf("createDirectory encode error: %v", err)
	}
}

func (h *APIHandler) handlePut(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	defer r.Body.Close()

	if path == "" {
		sendJSONError(w, "File name required", http.StatusBadRequest)
		return
	}
	// ContentLength may be -1 (chunked); WriteFile charges the bytes that
	// actually arrive and ignores the declared size for accounting.
	if err := ufs.WriteFile(path, r.Body, r.ContentLength); err != nil {
		log.Printf("handlePut WriteFile error: %v", err)
		if errors.Is(err, fs.ErrReadOnlyRoot) {
			sendJSONError(w, "Operation not permitted", http.StatusForbidden)
			return
		}
		if strings.Contains(err.Error(), "quota exceeded") {
			sendJSONError(w, "Quota exceeded", http.StatusInsufficientStorage)
			return
		}
		sendJSONError(w, "Upload failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status": "uploaded",
		"path":   path,
	}); err != nil {
		log.Printf("handlePut encode error: %v", err)
	}
}

func (h *APIHandler) handleDelete(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	if err := ufs.RemoveAll(ctx, path); err != nil {
		if errors.Is(err, fs.ErrReadOnlyRoot) {
			sendJSONError(w, "Operation not permitted on user root", http.StatusForbidden)
			return
		}
		sendJSONError(w, "Deletion failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *APIHandler) createShare(w http.ResponseWriter, ctx context.Context, path string, ufs *fs.UserFS) {
	if path == "" {
		sendJSONError(w, "Path required", http.StatusBadRequest)
		return
	}
	// Stat confirms the path exists and is inside the user root (resolvePath is called internally).
	if _, err := ufs.Stat(ctx, path); err != nil {
		sendJSONError(w, "Path not found", http.StatusNotFound)
		return
	}
	// Pass userRoot + relPath; CreateShare performs EvalSymlinks and boundary check.
	linkID, err := h.shareMgr.CreateShare(ufs.Root(), path)
	if err != nil {
		log.Printf("createShare error: %v", err)
		sendJSONError(w, "Sharing failed", http.StatusInternalServerError)
		return
	}

	// Both keys are emitted: "link" is what README documents, "share_url" is
	// what earlier builds returned and existing clients may parse.
	if err := json.NewEncoder(w).Encode(map[string]string{
		"link":      "/public/" + linkID,
		"share_url": "/public/" + linkID,
	}); err != nil {
		log.Printf("createShare encode error: %v", err)
	}
}

func sendJSONError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}); err != nil {
		log.Printf("sendJSONError encode error: %v", err)
	}
}

// getMimeType returns the MIME type for a file by extension,
// falling back to application/octet-stream for unknown types.
func getMimeType(info os.FileInfo) string {
	if info.IsDir() {
		return "inode/directory"
	}
	if t := mime.TypeByExtension(filepath.Ext(info.Name())); t != "" {
		return t
	}
	return "application/octet-stream"
}
