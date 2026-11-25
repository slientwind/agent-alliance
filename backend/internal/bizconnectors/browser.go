package bizconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (s Server) readBrowser(ctx context.Context, c Connector, secret Credentials, op Operation, input ReadRequest) (ReadResult, error) {
	if len(input.Arguments) != 0 || op.Method != http.MethodGet || !selectorPattern.MatchString(c.Settings.TableSelector) ||
		s.BrowserURL == "" || s.BrowserKey == "" {
		return ReadResult{}, ErrBadQuery
	}
	r, err := restRequest(ctx, c, secret, op, nil)
	if err != nil {
		return ReadResult{}, err
	}
	r.Header.Set("Accept", "text/html, application/xhtml+xml")
	reply, err := s.businessHTTP(ctx, r)
	if err != nil {
		return ReadResult{}, err
	}
	const maxHTML = 1 << 20
	media, _, err := mime.ParseMediaType(reply.Header.Get("Content-Type"))
	if err != nil || (media != "text/html" && media != "application/xhtml+xml") {
		reply.Body.Close()
		return ReadResult{}, ErrUnavailable
	}
	html, err := io.ReadAll(io.LimitReader(reply.Body, maxHTML+1))
	reply.Body.Close()
	if err != nil || len(html) == 0 || len(html) > maxHTML {
		return ReadResult{}, ErrUnavailable
	}
	limit := normalizeLimit(input.Limit)
	raw, err := json.Marshal(map[string]any{"html": string(html), "selector": c.Settings.TableSelector, "limit": limit})
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BrowserURL, "/")+"/table", bytes.NewReader(raw))
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+s.BrowserKey)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return ReadResult{}, ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return ReadResult{}, ErrUnavailable
	}
	value, err := boundedBusinessJSON(response)
	if err != nil {
		return ReadResult{}, err
	}
	return readBusinessRows(value, []string{"rows"}, limit)
}
