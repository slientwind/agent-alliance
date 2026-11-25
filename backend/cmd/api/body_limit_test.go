package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"alliance/internal/knowledge"
	"alliance/internal/studio"
)

func TestUploadBodyLimitsAreRouteSpecific(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		want         int64
	}{
		{http.MethodPost, "/api/studio/94d25ac1-82b8-46a3-a179-1acffa8c9e2f/chat", studio.MaxChatBody},
		{http.MethodGet, "/api/studio/94d25ac1-82b8-46a3-a179-1acffa8c9e2f/chat", 9 << 20},
		{http.MethodPost, "/api/studio/other/path/chat", 9 << 20},
		{http.MethodPost, "/api/studio", 9 << 20},
		{http.MethodPost, "/api/projects/example/sources", knowledge.AttachmentLimit + 1},
		{http.MethodPut, "/api/projects/example/sources/source", knowledge.AttachmentLimit + 1},
		{http.MethodGet, "/api/projects/example/sources/source", 9 << 20},
		{http.MethodPost, "/api/projects/example/sources/source/extract", 9 << 20},
		{http.MethodPost, "/api/projects/example/artifacts/source/file-revisions", 9 << 20},
		{http.MethodPost, "/projects/example/sources", 9 << 20},
	} {
		if got := requestBodyLimit(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
			t.Errorf("%s %s: limit %d, want %d", tt.method, tt.path, got, tt.want)
		}
	}
}
