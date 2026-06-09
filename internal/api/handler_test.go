package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nssc/internal/api"
	"nssc/internal/fs"
	"nssc/internal/users"
)

// newTestHandler wraps api.Handler the same way main.go does:
// http.StripPrefix("/api/", handler) so the handler receives paths
// without the /api/ prefix.
func newTestHandler(db *users.UsersDB, rootDir string, ufss *fs.UserFSServer) http.Handler {
	return http.StripPrefix("/api/", api.NewHandler(db, rootDir, ufss))
}

func TestAPIHandler(t *testing.T) {
	db := &users.UsersDB{}
	db.AddUser("user", "pass", "1GiB")
	mainQuota := fs.NewQuota(0)
	ufss, _ := fs.NewUserFSServer("/tmp/users", mainQuota, db.Users)
	handler := newTestHandler(db, "/tmp", ufss)

	t.Run("File upload", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/user/file.txt", strings.NewReader("content"))
		req.SetBasicAuth("user", "pass")
		req.ContentLength = int64(len("content"))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Errorf("Status code %d, want %d", w.Code, http.StatusCreated)
		}
	})

	t.Run("Directory listing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/user/", nil)
		req.SetBasicAuth("user", "pass")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Header().Get("Content-Type") != "application/json" {
			t.Error("Invalid content type")
		}
	})
}
