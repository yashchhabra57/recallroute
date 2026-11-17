// Package web embeds the board UI and its demo data into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the UI file tree rooted at static/.
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // the embed path is fixed at compile time
	}
	return sub
}
