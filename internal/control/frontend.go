package control

import (
	"embed"
	"io/fs"
)

//go:embed frontend/*
var frontend embed.FS

func FrontendFS() fs.FS {
	sub, err := fs.Sub(frontend, "frontend")
	if err != nil {
		return frontend
	}
	return sub
}
