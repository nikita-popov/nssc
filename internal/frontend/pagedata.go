package frontend

import (
	"nssc/internal/fs"
)

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
}
