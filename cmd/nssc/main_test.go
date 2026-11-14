package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nssc/internal/fs"
	"nssc/internal/users"
)

type testEnv struct {
	root   string
	srv    *httptest.Server
	ufs    *fs.UserFS
	client *http.Client
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	db := &users.UsersDB{}
	if err := db.AddUser("alice", "pass123", "10MiB"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := db.Save(filepath.Join(root, "db.json")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ufss, err := fs.NewUserFSServer(root, nil, db.Users)
	if err != nil {
		t.Fatalf("NewUserFSServer: %v", err)
	}
	ufs, err := ufss.GetUserFS("alice")
	if err != nil {
		t.Fatalf("GetUserFS: %v", err)
	}
	srv := httptest.NewServer(buildMux(db, root, ufss))
	t.Cleanup(srv.Close)
	// Do not follow redirects: the tests assert on 303 + Location.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &testEnv{root: root, srv: srv, ufs: ufs, client: client}
}

func (e *testEnv) do(t *testing.T, method, path string, body io.Reader, auth bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if auth {
		req.SetBasicAuth("alice", "pass123")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (e *testEnv) postForm(t *testing.T, path string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("alice", "pass123")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// The REST API used to reject every request because StripPrefix left a leading
// slash that SplitN turned into an empty username.
func TestAPIRoundTrip(t *testing.T) {
	e := newTestEnv(t)

	resp := e.do(t, http.MethodPut, "/api/alice/hello.txt", strings.NewReader("hello world"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/alice/hello.txt", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET file = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp); body != "hello world" {
		t.Errorf("body = %q, want %q", body, "hello world")
	}

	resp = e.do(t, http.MethodGet, "/api/alice/", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET dir = %d, want 200", resp.StatusCode)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(readBody(t, resp)), &entries); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(entries) != 1 || entries[0]["name"] != "hello.txt" {
		t.Errorf("unexpected listing: %+v", entries)
	}
}

func TestAPICreateDirectory(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPost, "/api/alice/docs/", nil, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST dir = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
	if info, err := os.Stat(filepath.Join(e.root, "user", "alice", "docs")); err != nil || !info.IsDir() {
		t.Fatalf("directory not created at canonical path: %v", err)
	}
}

// Deleting the user root via the API used to wipe the whole account.
func TestAPIRejectsDeleteOfRoot(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/api/alice/keep.txt", strings.NewReader("keep"), true)
	resp.Body.Close()

	resp = e.do(t, http.MethodDelete, "/api/alice/", nil, true)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE root = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/alice/keep.txt", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("file lost after root delete attempt: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// Share links were generated but unreachable: /public/ had no route.
func TestAPIShareProducesReachableLink(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/api/alice/shared.txt", strings.NewReader("public content"), true)
	resp.Body.Close()

	resp = e.do(t, http.MethodPost, "/api/alice/shared.txt/share", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST share = %d, want 200", resp.StatusCode)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(readBody(t, resp)), &payload); err != nil {
		t.Fatalf("decode share response: %v", err)
	}
	link := payload["link"]
	if link == "" {
		t.Fatalf("no link in response: %+v", payload)
	}
	if payload["share_url"] != link {
		t.Errorf("share_url = %q, want %q (backwards compat)", payload["share_url"], link)
	}

	// No authentication: a public link must work for anonymous clients.
	resp = e.do(t, http.MethodGet, link, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", link, resp.StatusCode)
	}
	if body := readBody(t, resp); body != "public content" {
		t.Errorf("public body = %q", body)
	}
}

func TestPublicHandlerRejectsPlainFiles(t *testing.T) {
	e := newTestEnv(t)
	publicDir := filepath.Join(e.root, "public")
	if err := os.MkdirAll(publicDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "notalink"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	resp := e.do(t, http.MethodGet, "/public/notalink", nil, false)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET plain file in public/ = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// WebDAV writes bypassed quota accounting because quotaWebDAVFile embedded
// *os.File, which promoted ReadFrom and let io.Copy skip Write.
func TestWebDAVQuotaAccounting(t *testing.T) {
	e := newTestEnv(t)
	payload := bytes.Repeat([]byte("a"), 1024)
	resp := e.do(t, http.MethodPut, "/webdav/alice/big.bin", bytes.NewReader(payload), true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("WebDAV PUT = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	_, used, _ := e.ufs.GetQuota()
	if used != 1024 {
		t.Errorf("quota used = %d, want 1024", used)
	}
}

// An upload of unknown length (ContentLength == -1) used to be rejected with
// 507 because used+(-1) underflowed into a huge uint64.
func TestWebDAVChunkedUpload(t *testing.T) {
	e := newTestEnv(t)
	payload := bytes.Repeat([]byte("b"), 512)
	req, err := http.NewRequest(http.MethodPut, e.srv.URL+"/webdav/alice/chunked.bin", io.NopCloser(bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1 // forces Transfer-Encoding: chunked
	req.SetBasicAuth("alice", "pass123")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("chunked PUT = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	_, used, _ := e.ufs.GetQuota()
	if used != 512 {
		t.Errorf("quota used = %d after chunked upload, want 512", used)
	}
}

// DELETE /webdav/alice/ used to return 204 and erase the entire account.
func TestWebDAVDeleteRootRefused(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/webdav/alice/keep.txt", strings.NewReader("keep"), true)
	resp.Body.Close()

	resp = e.do(t, http.MethodDelete, "/webdav/alice/", nil, true)
	if resp.StatusCode < 400 {
		t.Errorf("DELETE root = %d, want an error status", resp.StatusCode)
	}
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/alice/keep.txt", nil, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("account wiped by DELETE root: GET = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// The Share button posted name=... while the handler read path=..., so every
// click produced a 500.
func TestFrontendShareForm(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/api/alice/doc.txt", strings.NewReader("shared via ui"), true)
	resp.Body.Close()

	form := url.Values{"path": {"/doc.txt"}}
	resp = e.postForm(t, "/share", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /share = %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()

	loc, err := resp.Location()
	if err != nil {
		t.Fatalf("no Location header: %v", err)
	}
	id := loc.Query().Get("shared")
	if id == "" {
		t.Fatalf("no shared id in redirect: %s", loc.String())
	}

	resp = e.do(t, http.MethodGet, "/public/"+id, nil, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET public link = %d, want 200", resp.StatusCode)
	}
	if body := readBody(t, resp); body != "shared via ui" {
		t.Errorf("public body = %q", body)
	}
}

// Legacy pages posted name=/alice/doc.txt; that must keep working.
func TestFrontendShareLegacyFieldName(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/api/alice/legacy.txt", strings.NewReader("legacy"), true)
	resp.Body.Close()

	form := url.Values{"name": {"/alice/legacy.txt"}}
	resp = e.postForm(t, "/share", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /share (legacy field) = %d, want 303", resp.StatusCode)
	}
	resp.Body.Close()
}

// The search form submitted "query" but the handler read "q", so an empty
// pattern matched the whole tree. It also used to emit broken links.
func TestFrontendSearchIsLiteral(t *testing.T) {
	e := newTestEnv(t)
	for _, name := range []string{"hello.txt", "world.txt"} {
		resp := e.do(t, http.MethodPut, "/api/alice/"+name, strings.NewReader("x"), true)
		resp.Body.Close()
	}

	form := url.Values{"q": {"hello"}}
	resp := e.postForm(t, "/search", form)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /search = %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "hello.txt") {
		t.Errorf("search result missing hello.txt")
	}
	if strings.Contains(body, "world.txt") {
		t.Errorf("search for 'hello' matched world.txt")
	}
	if !strings.Contains(body, `href="/user/hello.txt"`) {
		t.Errorf("search result link is malformed")
	}
}

func TestFrontendSearchEmptyQueryMatchesNothing(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/api/alice/only.txt", strings.NewReader("x"), true)
	resp.Body.Close()

	form := url.Values{"q": {""}}
	resp = e.postForm(t, "/search", form)
	body := readBody(t, resp)
	if strings.Contains(body, "only.txt") {
		t.Errorf("empty query should not list the whole tree")
	}
}

// A leading-dot filename that is not a traversal must be accepted.
func TestWebDAVDotDotFilename(t *testing.T) {
	e := newTestEnv(t)
	resp := e.do(t, http.MethodPut, "/webdav/alice/..notes.txt", strings.NewReader("x"), true)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT ..notes.txt = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
}
