package web

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPProblemRedactsCredentialHeadersAndProviderErrors(t *testing.T) {
	const bearer, key, session = "fixture-bearer", "fixture-key-without-label", "fixture-session-cookie"
	request := httptest.NewRequest(http.MethodGet, "/api/fixture", nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("X-Fixture-Api-Key", key)
	request.AddCookie(&http.Cookie{Name: "test_session", Value: session})
	for _, message := range []string{bearer, key, session, `upstream echoed "api_key":"another-value"`, "密码：synthetic"} {
		recorder := httptest.NewRecorder()
		Handler(func(http.ResponseWriter, *http.Request) error { return Fail(400, message) }).ServeHTTP(recorder, request)
		body := recorder.Body.String()
		if recorder.Code != 400 || !strings.Contains(body, HiddenCredentials) || strings.Contains(body, message) || recorder.Header().Get("X-Request-ID") == "" {
			t.Fatal("credential-bearing errors must be hidden without losing status or the request ID")
		}
	}
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	recorder := httptest.NewRecorder()
	Handler(func(http.ResponseWriter, *http.Request) error { return errors.New("upstream " + key) }).ServeHTTP(recorder, request)
	if strings.Contains(logs.String(), key) || strings.Contains(recorder.Body.String(), key) || !strings.Contains(logs.String(), "error_type") {
		t.Fatal("the internal error retained its plaintext credential or lost its diagnostic type")
	}
	if got := safeProblemMessage(request, "请重新填写 API Key"); got != "请重新填写 API Key" {
		t.Fatal("an ordinary configuration message was hidden")
	}
}

func TestTraceMasksCredentialAssignmentsAndPreservesOperationalMetadata(t *testing.T) {
	for _, text := range []string{"X-Api-Key: synthetic", "Bearer fixture-auth", "a password in an excerpt", "sk-A1B2C3D4E5F6"} {
		if TraceText(text) != HiddenCredentials {
			t.Fatal("trace retained credential-bearing text")
		}
	}
	const value = "example-model-2026"
	if got := TraceText(value); got != value {
		t.Fatalf("unexpected change to model attribute: %q", got)
	}
}
