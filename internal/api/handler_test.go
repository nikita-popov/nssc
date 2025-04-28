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

func TestAPIHandler(t *testing.T) {
	db := &users.UsersDB{}
	db.AddUser("user", "pass", "1GiB")
	users, _ := db.GetUsers()
	mainQuota := fs.NewQuota(0, 0)
	ufss, _ := fs.NewUserFSServer("/tmp/users", mainQuota, users)
	handler := api.NewHandler(db, "/tmp", ufss)

	t.Run("File upload", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/user/file.txt", strings.NewReader("content"))
		req.SetBasicAuth("user", "pass")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Errorf("Status code %d, want %d", w.Code, http.StatusCreated)
		}
	})

	t.Run("Directory listing", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/user/", nil)
		req.SetBasicAuth("user", "pass")
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
		if w.Header().Get("Content-Type") != "application/json" {
			t.Error("Invalid content type")
		}
	})
}
