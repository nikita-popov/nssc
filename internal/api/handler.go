package api

import (
	"context"
	"encoding/json"
	"log"
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

func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer) *APIHandler {
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &APIHandler{
		db:       db,
		rootDir:  rootDir,
		shareMgr: shareMgr,
		fs:       fs,
	}
}

func (h *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// r.URL.Path is already stripped of "/api" prefix by http.StripPrefix in main.
	// Path format: /<username>[/<file-path>]
	fullPath := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(fullPath, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		sendJSONError(w, "Invalid request path", http.StatusBadRequest)
		return
	}

	name := parts[0]
	path := ""
	if len(parts) > 1 {
		path, _ = url.PathUnescape(parts[1])
	}

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
		sendJSONError(w, "FS error", http.StatusInternalServerError)
		return
	}

	ctx := context.Background()
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, ctx, path, ufs)
	case http.MethodPost:
		h.handlePost(w, r, ctx, path, ufs)
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
		h.listDirectory(w, ctx, path, ufs)
		return
	}

	f, err := ufs.Open(ctx, path)
	if err != nil {
		sendJSONError(w, "Failed to open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, info.Name(), info.ModTime(), f.(interface {
		io.ReadSeeker
	}))
}

func (h *APIHandler) listDirectory(w http.ResponseWriter, ctx context.Context, path string, ufs *fs.UserFS) {
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
	json.NewEncoder(w).Encode(response)
}

func (h *APIHandler) handlePost(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	if r.URL.Query().Get("mkdir") != "" {
		h.createDirectory(w, ctx, path, ufs)
		return
	}
	if r.URL.Query().Get("share") != "" {
		h.createShare(w, ctx, path, ufs)
		return
	}
	sendJSONError(w, "Invalid operation", http.StatusBadRequest)
}

func (h *APIHandler) createDirectory(w http.ResponseWriter, ctx context.Context, path string, ufs *fs.UserFS) {
	if err := ufs.MkdirAll(ctx, path, 0750); err != nil {
		sendJSONError(w, "Directory creation failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "created",
		"path":   path,
	})
}

func (h *APIHandler) handlePut(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	defer r.Body.Close()
	if err := ufs.WriteFile(path, r.Body, r.ContentLength); err != nil {
		log.Printf("handlePut WriteFile error: %v", err)
		sendJSONError(w, "Upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "uploaded",
		"path":   path,
	})
}

func (h *APIHandler) handleDelete(w http.ResponseWriter, r *http.Request, ctx context.Context, path string, ufs *fs.UserFS) {
	if err := ufs.RemoveAll(ctx, path); err != nil {
		sendJSONError(w, "Deletion failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIHandler) createShare(w http.ResponseWriter, ctx context.Context, path string, ufs *fs.UserFS) {
	// Validate path exists and is inside user's root before sharing.
	if _, err := ufs.Stat(ctx, path); err != nil {
		sendJSONError(w, "File not found", http.StatusNotFound)
		return
	}
	absPath := filepath.Join(ufs.Root(), filepath.Clean("/"+path))
	linkID, err := h.shareMgr.CreateShare(absPath)
	if err != nil {
		sendJSONError(w, "Sharing failed", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"share_url": "/public/" + linkID,
	})
}

func sendJSONError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

func getMimeType(info os.FileInfo) string {
	if info.IsDir() {
		return "inode/directory"
	}
	// TODO: http.DetectContentType()
	return "application/octet-stream"
}
