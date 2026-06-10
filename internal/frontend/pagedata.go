package frontend

import (
	"nssc/internal/fs"
)

// PageData holds all data passed to the HTML template.
type PageData struct {
	User          string
	CurrentPath   string
	ParentPath    string
	Files         []fs.FileEntry
	QuotaTotal    uint64
	QuotaTotalStr string
	QuotaUsed     uint64
	QuotaUsedStr  string
	SearchQuery   string
	FilesCount    int
	DirsCount     int
	// Version is the build-time version string injected via -ldflags "-X main.version=..."
	Version string
}
