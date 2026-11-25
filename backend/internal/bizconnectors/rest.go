package bizconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var pathValuePattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,159}$`)

func validOperations(operations []Operation) bool {
	if len(operations) > 150 {
		return false
	}
	seen := make(map[string]bool, len(operations))
	for _, op := range operations {
		if !operationPattern.MatchString(op.Name) || seen[op.Name] || !validText(op.Description, 900) ||
			!slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}, op.Method) ||
			(op.Risk != "read" && op.Risk != "write") || !approvedPath(op.Path, true) || len(op.Parameters) > 40 || len(op.ResultPath) > 8 {
			return false
		}
		seen[op.Name] = true
		for _, step := range op.ResultPath {
			if !identifierPattern.MatchString(step) {
				return false
			}
		}
		for name, param := range op.Parameters {
			if !identifierPattern.MatchString(name) || !slices.Contains([]string{"path", "query", "body"}, param.Location) ||
				!slices.Contains([]string{"string", "integer", "number", "boolean"}, param.Type) ||
				(param.Location == "path" && (!param.Required || !strings.Contains(op.Path, "{"+name+"}"))) ||
				(param.Location == "body" && (op.Method == "GET" || op.Method == "HEAD")) {
				return false
			}
		}
		for _, segment := range strings.Split(op.Path, "/") {
			if strings.HasPrefix(segment, "{") && op.Parameters[strings.Trim(segment, "{}")].Location != "path" {
				return false
			}
		}
	}
	return true
}

func validateArguments(operation Operation, arguments map[string]any) error {
	if len(arguments) > len(operation.Parameters) {
		return ErrBadQuery
	}
	for name, value := range arguments {
		param, ok := operation.Parameters[name]
		if !ok || !scalar(param.Type, value) {
			return ErrBadQuery
		}
	}
	for name, field := range operation.Parameters {
		if _, ok := arguments[name]; field.Required && !ok {
			return ErrBadQuery
		}
	}
	return nil
}

func readOperation(c Connector, name string) (Operation, error) {
	for _, op := range c.Operations {
		if op.Name == name && op.Risk == "read" {
			return op, nil
		}
	}
	return Operation{}, ErrBadQuery
}

func (s Server) businessHTTP(ctx context.Context, request *http.Request) (*http.Response, error) {
	var client *http.Client
	var err error
	if s.HTTP != nil {
		client, err = s.HTTP(ctx, request.URL.String())
	} else {
		base := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		client, err = s.Egress.Client(ctx, request.URL.String(), false, base)
	}
	if err != nil || client == nil {
		return nil, ErrUnavailable
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, ErrUnavailable
	}
	return response, nil
}

func boundedBusinessJSON(response *http.Response) (any, error) {
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBusinessReply+1))
	if err != nil || len(raw) > maxBusinessReply || !json.Valid(raw) {
		return nil, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}

func restRequest(ctx context.Context, connector Connector, secret Credentials, op Operation, arguments map[string]any) (*http.Request, error) {
	if err := validateArguments(op, arguments); err != nil {
		return nil, err
	}
	path, query, fields := op.Path, make(url.Values), make(map[string]any)
	for name, value := range arguments {
		switch op.Parameters[name].Location {
		case "path":
			encoded := fmt.Sprint(value)
			if !pathValuePattern.MatchString(encoded) {
				return nil, ErrBadQuery
			}
			path = strings.ReplaceAll(path, "{"+name+"}", encoded)
		case "query":
			query.Set(name, fmt.Sprint(value))
		case "body":
			fields[name] = value
		}
	}
	if !approvedPath(path, false) {
		return nil, ErrBadQuery
	}
	var body io.Reader
	if len(fields) > 0 {
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, ErrBadQuery
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := connector.Endpoint + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, op.Method, endpoint, body)
	if err != nil {
		return nil, ErrBadQuery
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if secret.Token != "" {
		request.Header.Set("Authorization", "Bearer "+secret.Token)
	} else if secret.Username != "" {
		request.SetBasicAuth(secret.Username, secret.Password)
	}
	return request, nil
}

func readBusinessRows(raw any, path []string, maximum int) (ReadResult, error) {
	for _, field := range path {
		object, ok := raw.(map[string]any)
		if !ok {
			return ReadResult{}, ErrUnavailable
		}
		raw, ok = object[field]
		if !ok {
			return ReadResult{}, ErrUnavailable
		}
	}
	out := ReadResult{Rows: []map[string]any{}}
	var rows []any
	switch value := raw.(type) {
	case []any:
		rows = value
	case map[string]any:
		rows = []any{value}
	case nil:
		return out, nil
	default:
		return out, ErrUnavailable
	}
	out.Truncated = len(rows) > maximum
	columns := make(map[string]bool)
	for _, rawRow := range rows[:min(len(rows), maximum)] {
		row, ok := rawRow.(map[string]any)
		if !ok || len(row) > 200 {
			return ReadResult{}, ErrUnavailable
		}
		out.Rows = append(out.Rows, row)
		for name := range row {
			if !columns[name] {
				columns[name] = true
				out.Columns = append(out.Columns, name)
			}
		}
	}
	slices.Sort(out.Columns)
	return out, nil
}

func (s Server) read(ctx context.Context, c Connector, credentials Credentials, input ReadRequest) (ReadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if c.Driver == "readonly_sql" {
		return s.readSQL(ctx, c, credentials, input)
	}
	op, err := readOperation(c, input.Operation)
	if err != nil || input.SQL != "" {
		return ReadResult{}, ErrBadQuery
	}
	if c.Driver == "browser_rpa_readonly" {
		return s.readBrowser(ctx, c, credentials, op, input)
	}
	var request *http.Request
	if c.Driver == "saas_pack" {
		request, op, err = saasReadRequest(ctx, c, credentials, op, input)
	} else {
		request, err = restRequest(ctx, c, credentials, op, input.Arguments)
	}
	if err != nil {
		return ReadResult{}, err
	}
	response, err := s.businessHTTP(ctx, request)
	if err != nil {
		return ReadResult{}, err
	}
	body, err := boundedBusinessJSON(response)
	if err != nil {
		return ReadResult{}, err
	}
	more := paginatedBusinessResponse(body, 0)
	if c.Driver == "saas_pack" {
		body, err = saasReply(c, op, body)
		if err != nil {
			return ReadResult{}, err
		}
	}
	result, err := readBusinessRows(body, op.ResultPath, normalizeLimit(input.Limit))
	result.Truncated = result.Truncated || more
	return result, err
}

func paginatedBusinessResponse(raw any, depth int) bool {
	object, ok := raw.(map[string]any)
	if !ok || depth > 4 {
		return false
	}
	for key, value := range object {
		switch key {
		case "done", "isLast":
			if value == false {
				return true
			}
		case "has_more", "has_next", "hasNext", "hasNextPage", "has_next_page":
			if value == true {
				return true
			}
		case "next_page_token", "nextRecordsUrl":
			if text, ok := value.(string); ok && text != "" {
				return true
			}
		}
		if nested, ok := value.(map[string]any); ok && paginatedBusinessResponse(nested, depth+1) {
			return true
		}
	}
	return false
}
