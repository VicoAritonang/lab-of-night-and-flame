// Package web embeds the built React UI so one binary serves the whole Lab.
// The Docker build copies frontend/dist here; local builds without it serve a
// placeholder page and the Vite dev server is used instead.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// Dist returns the UI root.
func Dist() fs.FS {
	sub, _ := fs.Sub(files, "dist")
	return sub
}
