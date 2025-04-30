package api

import (
	"encoding/json"
	"io"
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
	rootDir  string // TODO: remove
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
	fullPath := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.SplitN(fullPath, "/", 2)
	if len(parts) < 1 {
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
		log.Printf("User %s unauthorized ", username)
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		sendJSONError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user := h.db.GetUser(username)
	ufs, err := h.fs.GetUserFS(user.Name)
	if err != nil {
		log.Printf("User FS error: %s", err)
		sendJSONError(w, "FS error", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, path, ufs)
	case http.MethodPost:
		h.handlePost(w, r, path, ufs)
	case http.MethodPut:
		h.handlePut(w, r, path, ufs)
	case http.MethodDelete:
		h.handleDelete(w, r, path, ufs)
	default:
		sendJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleGet(w http.ResponseWriter, r *http.Request, absPath string, ufs *fs.UserFS) {
	info, err := os.Stat(absPath)
	if err != nil {
		sendJSONError(w, "Resource not found", http.StatusNotFound)
		return
	}

	if info.IsDir() {
		h.listDirectory(w, absPath, ufs)
		return
	}

	http.ServeFile(w, r, absPath)
}

func (h *APIHandler) listDirectory(w http.ResponseWriter, path string, ufs *fs.UserFS) {
	entries, err := os.ReadDir(path)
	if err != nil {
		sendJSONError(w, "Failed to read directory", http.StatusInternalServerError)
		return
	}

	response := make([]map[string]interface{}, 0)
	for _, entry := range entries {
		info, _ := entry.Info()
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

func (h *APIHandler) handlePost(w http.ResponseWriter, r *http.Request, path string, ufs *fs.UserFS) {
	if r.URL.Query().Get("mkdir") != "" {
		h.createDirectory(w, path, ufs)
		return
	}

	if r.URL.Query().Get("share") != "" {
		h.createShare(w, path)
		return
	}

	sendJSONError(w, "Invalid operation", http.StatusBadRequest)
}

func (h *APIHandler) createDirectory(w http.ResponseWriter, path string, ufs *fs.UserFS) {
	if err := os.MkdirAll(path, 0750); err != nil {
		sendJSONError(w, "Directory creation failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "created",
		"path":   path,
	})
}

func (h *APIHandler) handlePut(w http.ResponseWriter, r *http.Request, path string, ufs *fs.UserFS) {
	defer r.Body.Close()

	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		sendJSONError(w, "Path creation failed", http.StatusInternalServerError)
		return
	}

	file, err := os.Create(path)
	if err != nil {
		sendJSONError(w, "File creation failed", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if _, err := io.Copy(file, r.Body); err != nil {
		sendJSONError(w, "Upload failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "uploaded",
		"path":   path,
	})
}

func (h *APIHandler) handleDelete(w http.ResponseWriter, r *http.Request, path string, ufs *fs.UserFS) {
	if err := os.RemoveAll(path); err != nil {
		sendJSONError(w, "Deletion failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *APIHandler) createShare(w http.ResponseWriter, path string) {
	linkID, err := h.shareMgr.CreateShare(path)
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
