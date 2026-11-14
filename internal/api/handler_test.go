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

// newTestHandler wraps api.Handler exactly the way main.go does. The prefix
// must be "/api" (no trailing slash): with "/api/" the handler never saw the
// leading slash that the real mux produces, which is why the 401 regression
// went unnoticed.
func newTestHandler(db *users.UsersDB, rootDir string, ufss *fs.UserFSServer) http.Handler {
	return http.StripPrefix("/api", api.NewHandler(db, rootDir, ufss))
}

func TestAPIHandler(t *testing.T) {
	db := &users.UsersDB{}
	if err := db.AddUser("alice", "pass", "1GiB"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	ufss, err := fs.NewUserFSServer(t.TempDir(), fs.NewQuota(0), db.Users)
	if err != nil {
		t.Fatalf("NewUserFSServer: %v", err)
	}
	handler := newTestHandler(db, t.TempDir(), ufss)

	t.Run("File upload", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/alice/file.txt", strings.NewReader("content"))
		req.SetBasicAuth("alice", "pass")
		req.ContentLength = int64(len("content"))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Errorf("Status code %d, want %d", w.Code, http.StatusCreated)
		}
	})

	t.Run("Directory listing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/alice/", nil)
		req.SetBasicAuth("alice", "pass")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("Status code %d, want %d", w.Code, http.StatusOK)
		}
		if w.Header().Get("Content-Type") != "application/json" {
			t.Error("Invalid content type")
		}
	})

	t.Run("Unauthorized without credentials", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/alice/", nil)
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Status code %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})
}
