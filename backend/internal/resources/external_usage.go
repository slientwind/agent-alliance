package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"

	"github.com/jackc/pgx/v5"
)

type ExternalUsage struct {
	Input  *int64 `json:"inputTokens,omitempty"`
	Output *int64 `json:"outputTokens,omitempty"`
	Total  *int64 `json:"totalTokens,omitempty"`
	Cached *int64 `json:"cachedTokens,omitempty"`
}

type ExternalMetadata struct {
	Usage    *ExternalUsage
	TraceURL string
}

const externalTokenLimit int64 = 1_000_000_000_000

var tokenLikeTrace = regexp.MustCompile(`(?i)(access[_-]?token|api[_-]?key|password|secret|bearer|signature|sk-[a-z0-9_-]{10})`)

func traceAddress(value string) string {
	if value == "" || len(value) > 768 || tokenLikeTrace.MatchString(value) {
		return ""
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil ||
		target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || tokenLikeTrace.MatchString(target.Path) {
		return ""
	}
	return target.String()
}

func externalMetadata(headers http.Header, usage *ExternalUsage) ExternalMetadata {
	if usage == nil {
		usage = parseExternalUsage([]byte(headers.Get("X-Alliance-Usage")))
	}
	return ExternalMetadata{Usage: usage, TraceURL: traceAddress(headers.Get("X-Alliance-Trace-Url"))}
}

func externalUsageFromPayload(payload json.RawMessage) *ExternalUsage {
	for _, path := range []string{"$.usage", "$.metadata.usage", "$.result.usage", "$.result.metadata.usage"} {
		if usage := parseExternalUsage(readJSONPath(payload, path)); usage != nil {
			return usage
		}
	}
	return nil
}

func parseExternalUsage(raw []byte) *ExternalUsage {
	if len(raw) == 0 || len(raw) > 16<<10 {
		return nil
	}
	var in struct {
		Input         *int64 `json:"input_tokens"`
		Prompt        *int64 `json:"prompt_tokens"`
		InputCount    *int64 `json:"input_count"`
		Output        *int64 `json:"output_tokens"`
		Completion    *int64 `json:"completion_tokens"`
		OutputCount   *int64 `json:"output_count"`
		Total         *int64 `json:"total_tokens"`
		TokenCount    *int64 `json:"token_count"`
		Cached        *int64 `json:"cached_tokens"`
		CacheHit      *int64 `json:"prompt_cache_hit_tokens"`
		PromptDetails struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	return validateExternalUsage(&ExternalUsage{
		Input: firstInt(in.Input, in.Prompt, in.InputCount), Output: firstInt(in.Output, in.Completion, in.OutputCount),
		Total: firstInt(in.Total, in.TokenCount), Cached: firstInt(in.PromptDetails.Cached, in.Cached, in.CacheHit),
	})
}

func firstInt(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func externalInt(value *int) *int64 {
	if value == nil {
		return nil
	}
	v := int64(*value)
	return &v
}

func validateExternalUsage(usage *ExternalUsage) *ExternalUsage {
	if usage.Input == nil && usage.Output == nil && usage.Total == nil && usage.Cached == nil {
		return nil
	}
	for _, value := range []*int64{usage.Input, usage.Output, usage.Total, usage.Cached} {
		if value != nil && (*value < 0 || *value > externalTokenLimit) {
			return nil
		}
	}
	if usage.Cached != nil && usage.Input != nil && *usage.Cached > *usage.Input {
		return nil
	}
	return usage
}

type ExternalReceiptRef struct {
	Request, Run, Trial, Operation string
	Turn, Attempt                  int
}

func RecordExternalReceipt(ctx context.Context, tx pgx.Tx, ref ExternalReceiptRef, metadata ExternalMetadata) error {
	disclosure := "undisclosed"
	var usage json.RawMessage
	if metadata.Usage != nil {
		raw, err := json.Marshal(metadata.Usage)
		if err != nil {
			return err
		}
		disclosure, usage = "reported", raw
	}
	_, err := tx.Exec(ctx, `insert into external_agent_receipts(request_key,run_id,trial_id,turn,attempt,operation,cost_disclosure,usage,trace_url)
		values($1,nullif($2,'')::uuid,nullif($3,'')::uuid,nullif($4,0),nullif($5,0),$6,$7,$8,nullif($9,'')) on conflict(request_key) do nothing`,
		ref.Request, ref.Run, ref.Trial, ref.Turn, ref.Attempt, ref.Operation, disclosure, usage, metadata.TraceURL)
	return err
}
