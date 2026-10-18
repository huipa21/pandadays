// Package locales embeds the UI translation files (one flat JSON object per language).
package locales

import (
	"embed"
	"io/fs"
)

//go:embed *.json
var files embed.FS

func FS() fs.FS { return files }
