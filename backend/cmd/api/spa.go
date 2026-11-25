package main

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func spaHandler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if name == "/api" || strings.HasPrefix(name, "/api/") {
			http.NotFound(w, r)
			return
		}
		if name != "/" {
			_, err := fs.Stat(root, strings.TrimPrefix(name, "/"))
			if !errors.Is(err, fs.ErrNotExist) || name == "/assets" || strings.HasPrefix(name, "/assets/") || path.Ext(name) != "" {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, root, "index.html")
	})
}
