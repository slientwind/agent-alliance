package resources

import (
	"net/http"
	"testing"
)

func TestExternalUsagePrefersBodyAndValidatesDisclosure(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-Alliance-Usage", `{"total_tokens":120}`)
	headers.Set("X-Alliance-Trace-Url", "https://traces.example.test/run/ab")
	standard := []byte(`{"result":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}}}`)
	receipt := externalMetadata(headers, externalUsageFromPayload(standard))
	if receipt.Usage == nil || receipt.Usage.Total == nil || *receipt.Usage.Total != 5 ||
		receipt.Usage.Cached == nil || *receipt.Usage.Cached != 1 || receipt.TraceURL != "https://traces.example.test/run/ab" {
		t.Fatalf("valid standard metadata did not take precedence: %+v", receipt)
	}
	malformed := []byte(`{"usage":{"input_tokens":2,"output_tokens":1,"cached_tokens":9}}`)
	fallback := externalMetadata(headers, externalUsageFromPayload(malformed))
	if fallback.Usage == nil || fallback.Usage.Total == nil || *fallback.Usage.Total != 120 {
		t.Fatalf("header fallback: %+v", fallback)
	}
	for _, url := range []string{
		"javascript:alert(1)", "https://username:password@traces.example.test/ab",
		"https://traces.example.test/ab?token=fixture", "https://traces.example.test/sk-fixturex99999999",
	} {
		headers.Set("X-Alliance-Trace-Url", url)
		if got := externalMetadata(headers, nil).TraceURL; got != "" {
			t.Errorf("unsafe or credential-bearing trace URL retained: %q", got)
		}
	}
	headers.Set("X-Alliance-Usage", `{"total_tokens":-1}`)
	if got := externalMetadata(headers, nil).Usage; got != nil {
		t.Fatalf("negative usage disclosed: %+v", got)
	}
	headers.Set("X-Alliance-Usage", `{"total_tokens":0}`)
	if got := externalMetadata(headers, nil).Usage; got == nil || got.Total == nil || *got.Total != 0 {
		t.Fatalf("valid zero-token disclosure lost: %+v", got)
	}
}
