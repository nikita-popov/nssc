package frontend

import (
	"context"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/dustin/go-humanize"

	"nssc/internal/fs"
	"nssc/internal/share"
	"nssc/internal/users"
)

var tpl = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8" />
	<title>nssc - {{ .CurrentPath }}</title>
	<style>
        body { margin: 0 auto; font-family: 'Courier New', Courier, monospace; }
        td { font-size: 14px; }
        a { text-decoration: none; }
        .userform { padding: 4px; }
        .loginform { display: grid; }
        form, label, table { margin: auto; }
        div { align-items: center; display: grid; }
        input, .abutton { margin: auto; border: 1px solid; border-radius: 8px; }
        header, footer, .fds { display: flex; justify-content: center; text-decoration: auto; }
        table { max-width: 50%; }
        tr:nth-child(even) { background-color: lightgray; }
        .page { margin: 0.2rem; }
        .pages { display: flex; }
	</style>
</head>
<body>

<div>
<table>
  <tbody>
    {{ if .ParentPath }}
    <tr>
      <td></td>
      <td><a href="/{{ .User }}{{ .ParentPath }}">..</a></td>
      <td></td>
      <td></td>
      <td></td>
      <td></td>
    </tr>
    {{ end }}
    {{ range .Files }}
    <tr>
      <td><input type="checkbox" form="rm" name="path" value="{{ .RelPath }}"></td>
      <td>
          {{ if .IsDir }}
          <a href="/{{ $.User }}{{ .RelPath }}/">{{ .Name }}</a>
          {{ else }}
          <a href="/{{ $.User }}{{ .RelPath }}">{{ .Name }}</a>
          {{ end }}
      </td>
      <td>
          {{ if not .IsDir }}
          {{ .Size }}
          {{ end }}
      </td>
      <td>{{ .ModTime }}</td>
      <td>
        {{ if not .IsDir }}
          <form method="post" action="/share">
              <input type="hidden" name="name" value="/{{ $.User }}{{ .RelPath }}">
              <input type="submit" value="Share">
          </form>
        {{ end }}
      </td>
      <td>{{ if not .IsDir }}<a href="/{{ $.User }}{{ .RelPath }}?preview=1">Preview</a>{{ end }}</td>
    </tr>
    {{ end }}
  </tbody>
</table>
</div>

<div class="userform">
<form action="/search" method="get">
  <input type="text" name="q" placeholder="Search term" value="{{ .SearchQuery }}">
  <input type="submit" value="Search">
</form>
</div>

<div class="userform">
<form action="/upload" method="post" enctype="multipart/form-data">
  <input type="hidden" name="path" value="{{ .CurrentPath }}">
  <input type="file" name="file" required>
  <input type="submit" value="Upload">
</form>
</div>

<div class="userform">
<form action="/mkdir" method="post">
  <input type="hidden" name="path" value="{{ .CurrentPath }}">
  <input type="text" name="dirname" placeholder="Directory name" required>
  <input type="submit" value="Create">
</form>
</div>

<div class="userform">
<form id="rm" method="post" action="/rm">
    <input type="hidden" name="dir" value="{{ .CurrentPath }}">
    <input type="submit" value="Remove selected files">
</form>
</div>

{{ if .QuotaTotal }}
<div class="userform">
  <label>
    User quota:
    <progress value="{{ .QuotaUsed }}" max="{{ .QuotaTotal }}">{{ .QuotaUsedStr }} / {{ .QuotaTotalStr }}</progress>
    {{ .QuotaUsedStr }} / {{ .QuotaTotalStr }}
  </label>
</div>
{{ end }}

<div class="userform">
<form method="post" action="/logout">
  <input type="submit" value="Logout">
</form>
</div>

<div class="userform">
<span class="fds">{{ .DirsCount }} directories, {{ .FilesCount }} files</span>
</div>

<footer>
nssc
<footer>

</body>
</html>
`))

type FrontendHandler struct {
	ctx      context.Context
	db       *users.UsersDB
	rootDir  string // TODO: remove
	shareMgr *share.ShareManager
	template *template.Template
	fs       *fs.UserFSServer
}

func NewHandler(db *users.UsersDB, rootDir string, fs *fs.UserFSServer) *FrontendHandler {
	shareMgr := share.NewShareManager(filepath.Join(rootDir, "public"))
	return &FrontendHandler{db: db, rootDir: rootDir, shareMgr: shareMgr, template: tpl, fs: fs}
}

func (h *FrontendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO: cookie
	username, pass, ok := r.BasicAuth()
	if !ok || !h.db.Authenticate(username, pass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	user := h.db.GetUser(username)
	ufs, err := h.fs.GetUserFS(user.Name)
	if err != nil {
		// TODO
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/upload":
			h.handleUpload(w, r, username, ufs)
			return
		case "/mkdir":
			h.handleMkdir(w, r, username, ufs)
			return
		case "/rm":
			h.handleDelete(w, r, username, ufs)
			return
		case "/logout":
			h.handleLogout(w, r, username)
			return
		case "/share":
			h.handleShare(w, r, username, ufs)
			return
		}
	}

	if r.URL.Path == "/" {
		h.rootHandler(w, r)
		return
	}
	h.userHandler(w, r, username, ufs)
}

func (h *FrontendHandler) rootHandler(w http.ResponseWriter, r *http.Request) {
	username, _, ok := r.BasicAuth()
	if !ok || username == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/"+username, http.StatusSeeOther)
}

func (h *FrontendHandler) userHandler(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	pathPrefix := "/" + user
	relPath := strings.TrimPrefix(r.URL.Path, pathPrefix)

	decodedPath, err := url.PathUnescape(relPath)
	if err != nil {
		http.Error(w, "Invalid path encoding", http.StatusBadRequest)
		return
	}
	curPath := filepath.Clean(decodedPath)

	fi, err := ufs.Stat(h.ctx, decodedPath)
	if err != nil {
		log.Printf("Path %s stat error: %v", decodedPath, err)
		http.Error(w, "Forbidden path", http.StatusForbidden)
		return
	}
	if !fi.IsDir() {
		f, err := ufs.Open(h.ctx, decodedPath)
		if err != nil {
			log.Printf("Path %s open error: %v", decodedPath, err)
			http.Error(w, "Forbidden path", http.StatusForbidden)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "File serving not supported", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
		return
	}

	files, err := ufs.ReadDir(decodedPath)
	if err != nil {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
	}

	type FileEntry struct {
		Name    string
		RelPath string
		IsDir   bool
		Size    string
		ModTime string
	}
	var (
		fileEntries []FileEntry
		filesCount  int
		dirsCount   int
	)
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			log.Printf("File info error: %v", err)
			continue
		}
		size := ""
		if !info.IsDir() {
			size = humanize.IBytes(uint64(info.Size()))
		}
		modTime := info.ModTime().Format("2006-01-02T15:04:05+0000")
		rel := filepath.Join(curPath, f.Name())
		if info.IsDir() {
			dirsCount++
		} else {
			filesCount++
		}
		fileEntries = append(fileEntries, FileEntry{
			Name:    f.Name(),
			RelPath: rel,
			IsDir:   info.IsDir(),
			Size:    size,
			ModTime: modTime,
		})
	}

	parentPath := ""
	if curPath != "" {
		parentPath = filepath.Dir(curPath)
		if curPath == "/" {
			parentPath = ""
		}
	}

	searchQuery := ""

	quotaUsed, quotaTotal, _ := ufs.GetQuota()
	quotaTotalStr := humanize.IBytes(uint64(quotaTotal))
	quotaUsedStr := humanize.IBytes(uint64(quotaUsed))

	data := struct {
		User          string
		CurrentPath   string
		ParentPath    string
		Files         []FileEntry
		QuotaTotal    uint64
		QuotaTotalStr string
		QuotaUsed     uint64
		QuotaUsedStr  string
		SearchQuery   string
		FilesCount    int
		DirsCount     int
	}{
		User:          user,
		CurrentPath:   curPath,
		ParentPath:    parentPath,
		Files:         fileEntries,
		QuotaTotal:    uint64(quotaTotal),
		QuotaTotalStr: quotaTotalStr,
		QuotaUsed:     uint64(quotaUsed),
		QuotaUsedStr:  quotaUsedStr,
		SearchQuery:   searchQuery,
		FilesCount:    filesCount,
		DirsCount:     dirsCount,
	}

	if err := h.template.Execute(w, data); err != nil {
		log.Println("Template execute error:", err)
	}
}

func (h *FrontendHandler) handleUpload(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	// TODO: Size
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		log.Printf("Form parse error: %v", err)
		http.Error(w, "Form parse error: "+err.Error(), http.StatusBadRequest)
		return
	}

	curPath := r.FormValue("path")
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Form file error: %v", err)
		http.Error(w, "File error: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	dstPath := filepath.Join(curPath, header.Filename)

	err = ufs.WriteFile(dstPath, file, header.Size)
	if err != nil {
		log.Printf("File saving error: %v", err)
		http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Form file %s saved to %s", header.Filename, dstPath)

	redirectURL := "/" + user + "/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *FrontendHandler) handleMkdir(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}

	curPath := r.FormValue("path")
	dirname := r.FormValue("dirname")
	if dirname == "" {
		log.Printf("Malformed form: Directory name required", )
		http.Error(w, "Directory name required", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(curPath, dirname)

	err = ufs.Mkdir(h.ctx, fullPath, 0755)
	if err != nil {
		log.Printf("Mkdir error: %v", err)
		http.Error(w, "Create error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Directory %s created in %s", dirname, fullPath)

	redirectURL := "/" + user + "/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *FrontendHandler) handleDelete(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Form parse error", http.StatusBadRequest)
		return
	}

	paths := r.Form["path"]
	curPath := r.FormValue("dir")

	for _, p := range paths {
		path := filepath.Join(curPath, p)
		if err := ufs.RemoveAll(h.ctx, path); err != nil {
			http.Error(w, "Delete error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("File %s removed", path)
	}

	redirectURL := "/" + user + "/" + curPath
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *FrontendHandler) handleLogout(w http.ResponseWriter, r *http.Request, username string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="CloudStorage"`)
	http.Error(w, "Logged out", http.StatusUnauthorized)
	log.Printf("User %s logged out", username)
}

func (h *FrontendHandler) handleShare(w http.ResponseWriter, r *http.Request, user string, ufs *fs.UserFS) {
	path := r.FormValue("name")
	userRoot := filepath.Join(h.rootDir, "users", user)
	absPath := filepath.Join(userRoot, strings.TrimPrefix(path, "/"+user+"/")) // TODO: to ufs

	linkID, err := h.shareMgr.CreateShare(absPath)
	if err != nil {
		http.Error(w, "Share error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Share for %s created at %s", path, linkID)

	redirectURL := filepath.Dir(path)
	http.Redirect(w, r, redirectURL+"?share="+linkID, http.StatusSeeOther)
}
