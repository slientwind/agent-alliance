package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ready"))
	})
	// IM callbacks dispatch through a nested mux for all provider methods.
	callbacks := http.NewServeMux()
	callbacks.HandleFunc("POST /api/channels/feishu/events", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/api/channels/feishu/", callbacks)
	mux.HandleFunc("POST /internal/project-stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	routes := withWorkspaceFiles(mux, fstest.MapFS{
		"index.html":    {Data: []byte("<html>Alliance</html>")},
		"assets/app.js": {Data: []byte("console.log('ready')")},
		"manifest":      {Data: []byte("static manifest")},
	})

	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/", "<html>Alliance</html>", 200},
		{"GET", "/chat/cf3b1c83?tab=history", "<html>Alliance</html>", 200},
		{"GET", "/agents/", "<html>Alliance</html>", 200},
		{"GET", "/admin/monitor", "<html>Alliance</html>", 200},
		{"HEAD", "/admin/monitor", "", 200},
		{"GET", "/assets/app.js", "console.log('ready')", 200},
		{"GET", "/manifest", "static manifest", 200},
		{"GET", "/assets/missing", "404 page not found\n", 404},
		{"GET", "/favicon.ico", "404 page not found\n", 404},
		{"GET", "/api/health", "ready", 200},
		{"GET", "/api", "404 page not found\n", 404},
		{"GET", "/api/missing", "404 page not found\n", 404},
		{"POST", "/chat", "Method Not Allowed\n", 405},
		{"POST", "/api/health", "Method Not Allowed\n", 405},
		{"POST", "/api/channels/feishu/events", "", 204},
		{"GET", "/api/channels/feishu/events", "Method Not Allowed\n", 405},
		{"POST", "/internal/project-stream", "", 204},
		{"GET", "/internal/missing", "404 page not found\n", 404},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), tc.status, tc.body)
			}
			if strings.HasPrefix(tc.body, "<html>") && !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("unexpected content type %q", w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestSPAFallbackMissingIndex(t *testing.T) {
	w := httptest.NewRecorder()
	spaHandler(fstest.MapFS{"asset.txt": {Data: []byte("asset")}}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}
