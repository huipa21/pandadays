// Package web embeds HTML templates and static assets.
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates static
var files embed.FS

func Templates() fs.FS {
	sub, _ := fs.Sub(files, "templates")
	return sub
}

func Static() fs.FS {
	sub, _ := fs.Sub(files, "static")
	return sub
}
