package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"alliance/internal/egress"
	"alliance/internal/resources"
)

const (
	Tavily          = "tavily"
	Bocha           = "bocha"
	credentialScope = "integration/web-search/"
	resultLimit     = 6
	responseLimit   = 512 << 10
)

var ErrUnavailable = errors.New("联网搜索暂不可用")

type Client struct {
	Vault  resources.Vault
	Egress egress.Policy
	HTTP   *http.Client
}

type Config struct {
	Provider, Endpoint string
	Credential         []byte
}

type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func DefaultEndpoint(provider string) string {
	switch provider {
	case Tavily:
		return "https://api.tavily.com/search"
	case Bocha:
		return "https://api.bochaai.com/v1/web-search"
	default:
		return ""
	}
}

func ValidQuery(query string) bool {
	return query != "" && len(query) <= 1280 && utf8.ValidString(query) &&
		utf8.RuneCountInString(query) <= 320 && !strings.ContainsFunc(query, unicode.IsControl)
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && len(raw) <= 512 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.Path != "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func credentialContext(config Config) string {
	return credentialScope + config.Provider + "/" + config.Endpoint
}

func (c Client) Search(ctx context.Context, config Config, query string, allowed func(context.Context) bool) ([]Result, error) {
	if !ValidQuery(query) || !validEndpoint(config.Endpoint) || len(config.Credential) == 0 {
		return nil, ErrUnavailable
	}
	var payload map[string]any
	switch config.Provider {
	case Tavily:
		payload = map[string]any{"query": query, "max_results": resultLimit, "search_depth": "basic",
			"include_answer": false, "include_raw_content": false, "auto_parameters": false}
	case Bocha:
		payload = map[string]any{"query": query, "count": resultLimit, "summary": false}
	default:
		return nil, ErrUnavailable
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	client, err := c.Egress.Client(ctx, config.Endpoint, false, c.HTTP)
	if err != nil {
		return nil, ErrUnavailable
	}
	bearer, err := c.Vault.Open(config.Credential, credentialContext(config))
	if err != nil || bearer == "" {
		return nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if allowed != nil && !allowed(ctx) {
		return nil, ErrUnavailable
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > responseLimit {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return nil, ErrUnavailable
	}
	results, err := parseResults(config.Provider, raw)
	if err != nil || (allowed != nil && !allowed(ctx)) {
		return nil, ErrUnavailable
	}
	return results, nil
}

func parseResults(provider string, body []byte) ([]Result, error) {
	results := make([]Result, 0, resultLimit)
	switch provider {
	case Tavily:
		var response struct {
			Results []struct {
				Title, URL, Content string
			}
		}
		if json.Unmarshal(body, &response) != nil || response.Results == nil {
			return nil, ErrUnavailable
		}
		for _, item := range response.Results {
			results = appendResult(results, item.Title, item.URL, item.Content)
		}
	case Bocha:
		var response struct {
			Code int
			Data json.RawMessage
		}
		if json.Unmarshal(body, &response) != nil || (response.Code != 0 && response.Code != 200) {
			return nil, ErrUnavailable
		}
		if len(response.Data) != 0 {
			body = response.Data
		}
		var pages struct {
			Type     string `json:"_type"`
			WebPages *struct {
				Value []struct {
					Name, URL, Snippet string
				}
			}
		}
		if json.Unmarshal(body, &pages) != nil || (pages.Type != "SearchResponse" && pages.WebPages == nil) {
			return nil, ErrUnavailable
		}
		if pages.WebPages != nil {
			for _, item := range pages.WebPages.Value {
				results = appendResult(results, item.Name, item.URL, item.Snippet)
			}
		}
	default:
		return nil, ErrUnavailable
	}
	return results, nil
}

func appendResult(results []Result, title, rawURL, snippet string) []Result {
	if len(results) == resultLimit || len(rawURL) > 1536 {
		return results
	}
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "https" && target.Scheme != "http") || target.Hostname() == "" || target.User != nil {
		return results
	}
	for _, result := range results {
		if result.URL == rawURL {
			return results
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = target.Hostname()
	}
	return append(results, Result{Title: truncate(title, 160), URL: rawURL, Snippet: truncate(strings.Join(strings.Fields(snippet), " "), 520)})
}

func truncate(value string, limit int) string {
	for offset := range value {
		if offset >= limit {
			return value[:offset]
		}
	}
	return value
}
