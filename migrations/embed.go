// Package migrations embeds the numbered SQL schema migrations.
//
// Files are named NNNN_description.sql and applied in numeric order.
// Never edit a migration once it has been applied; add a new one instead.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

func FS() fs.FS { return files }
