package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distEmbedFS embed.FS

// DistFS returns the embedded web/dist filesystem for single-binary Web UI hosting.
func DistFS() (fs.FS, error) {
	return fs.Sub(distEmbedFS, "dist")
}
