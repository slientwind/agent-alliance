package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type KBInference interface {
	EmbeddingRevision(context.Context) (string, error)
	Embed(context.Context, []string) (string, []json.RawMessage, error)
	Rerank(context.Context, string, []string) (string, []float64, error)
}

type kbInferenceClient struct {
	endpoint, token string
	client          *http.Client
}

func ConfiguredKBInference() KBInference {
	endpoint, token := strings.TrimRight(os.Getenv("RETRIEVAL_URL"), "/"), os.Getenv("CONNECTOR_TOKEN")
	if endpoint == "" || token == "" {
		return nil
	}
	return kbInferenceClient{endpoint: endpoint, token: token, client: &http.Client{
		Timeout: 18 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var (
	kbEmbedRevision = regexp.MustCompile(`^bge-m3/[0-9a-f]{20}$`)
	kbRankRevision  = regexp.MustCompile(`^bge-reranker-v2-m3/[0-9a-f]{20}$`)
	errKBInference  = errors.New("knowledge inference unavailable")
)

func (c kbInferenceClient) call(ctx context.Context, route string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 131072 {
		return errKBInference
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+route, bytes.NewReader(raw))
	if err != nil {
		return errKBInference
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return errKBInference
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errKBInference
	}
	const maximumReply = 1 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maximumReply+1))
	if err != nil || len(data) > maximumReply || json.Unmarshal(data, out) != nil {
		return errKBInference
	}
	return nil
}

func (c kbInferenceClient) EmbeddingRevision(ctx context.Context) (string, error) {
	var status struct{ Embedding string }
	if err := c.call(ctx, "/kb/status", struct{}{}, &status); err != nil || !kbEmbedRevision.MatchString(status.Embedding) {
		return "", errKBInference
	}
	return status.Embedding, nil
}

func checkedVector(raw json.RawMessage) bool {
	var values []float64
	if json.Unmarshal(raw, &values) != nil || len(values) != 1024 {
		return false
	}
	var sum float64
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1.002 {
			return false
		}
		sum += value * value
	}
	return sum >= 0.99 && sum <= 1.01
}

func (c kbInferenceClient) Embed(ctx context.Context, texts []string) (string, []json.RawMessage, error) {
	if len(texts) == 0 || len(texts) > 8 {
		return "", nil, errKBInference
	}
	var out struct {
		Encoder string
		Vectors []json.RawMessage
	}
	if err := c.call(ctx, "/kb/embed", map[string][]string{"texts": texts}, &out); err != nil || !kbEmbedRevision.MatchString(out.Encoder) || len(out.Vectors) != len(texts) {
		return "", nil, errKBInference
	}
	for _, vector := range out.Vectors {
		if !checkedVector(vector) {
			return "", nil, errKBInference
		}
	}
	return out.Encoder, out.Vectors, nil
}

func (c kbInferenceClient) Rerank(ctx context.Context, query string, texts []string) (string, []float64, error) {
	if len(texts) == 0 || len(texts) > 24 {
		return "", nil, errKBInference
	}
	var out struct {
		Reranker string
		Scores   []float64
	}
	if err := c.call(ctx, "/kb/rerank", map[string]any{"query": query, "texts": texts}, &out); err != nil ||
		!kbRankRevision.MatchString(out.Reranker) || len(out.Scores) != len(texts) {
		return "", nil, errKBInference
	}
	for _, score := range out.Scores {
		if math.IsNaN(score) || score < 0 || score > 1 {
			return "", nil, errKBInference
		}
	}
	return out.Reranker, out.Scores, nil
}
