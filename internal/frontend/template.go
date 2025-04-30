package frontend

import (
	"html/template"
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
Powered by nssc
<footer>

</body>
</html>
`))
