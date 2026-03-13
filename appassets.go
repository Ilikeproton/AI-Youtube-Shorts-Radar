package appassets

import (
	"embed"
	"io/fs"
)

//go:embed all:web-dist
var files embed.FS

func WebDist() fs.FS {
	sub, err := fs.Sub(files, "web-dist")
	if err != nil {
		panic(err)
	}
	return sub
}
