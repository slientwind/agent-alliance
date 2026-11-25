package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"alliance/internal/studio"
)

func TestStudioUploadBodyLimitIsRouteSpecific(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		want         int64
	}{
		{http.MethodPost, "/api/studio/94d25ac1-82b8-46a3-a179-1acffa8c9e2f/chat", studio.MaxChatBody},
		{http.MethodGet, "/api/studio/94d25ac1-82b8-46a3-a179-1acffa8c9e2f/chat", 9 << 20},
		{http.MethodPost, "/api/studio/other/path/chat", 9 << 20},
		{http.MethodPost, "/api/studio", 9 << 20},
		{http.MethodPost, "/api/projects/example/sources", 9 << 20},
	} {
		if got := requestBodyLimit(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
			t.Errorf("%s %s: limit %d, want %d", tt.method, tt.path, got, tt.want)
		}
	}
}
