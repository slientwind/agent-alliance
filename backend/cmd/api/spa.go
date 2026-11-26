package main

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Use a separate mux for browser routes so an HTTP method-specific SPA
// fallback cannot conflict with an entire API subtree.
func withWorkspaceFiles(application http.Handler, root fs.FS) http.Handler {
	workspace := http.NewServeMux()
	workspace.Handle("GET /", spaHandler(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
		if name == "/api" || strings.HasPrefix(name, "/api/") ||
			name == "/internal" || strings.HasPrefix(name, "/internal/") {
			application.ServeHTTP(w, r)
			return
		}
		workspace.ServeHTTP(w, r)
	})
}

func spaHandler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)
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
