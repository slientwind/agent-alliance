package bizconnectors

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxBusinessRows = 200
const maxBusinessReply = 2 << 20

var ErrUnavailable = errors.New("business connector unavailable")
var ErrBadQuery = errors.New("invalid governed read query")

type Parameter struct {
	Type     string `json:"type"`
	Location string `json:"location"`
	Required bool   `json:"required,omitempty"`
}

type Operation struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Method      string               `json:"method"`
	Path        string               `json:"path"`
	Risk        string               `json:"risk"`
	Parameters  map[string]Parameter `json:"parameters,omitempty"`
	ResultPath  []string             `json:"resultPath,omitempty"`
}

type Settings struct {
	Database         string            `json:"database,omitempty"`
	AllowedRelations []string          `json:"allowedRelations,omitempty"`
	Resources        map[string]string `json:"resources,omitempty"`
	BrowserPath      string            `json:"browserPath,omitempty"`
	TableSelector    string            `json:"tableSelector,omitempty"`
}

type Credentials struct {
	Token     string `json:"token,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	AppKey    string `json:"appKey,omitempty"`
	AppSecret string `json:"appSecret,omitempty"`
	Account   string `json:"account,omitempty"`
}

type Connector struct {
	ID         string      `json:"id"`
	Space      string      `json:"spaceId"`
	Category   string      `json:"category"`
	Driver     string      `json:"driver"`
	Provider   string      `json:"provider"`
	Name       string      `json:"name"`
	Endpoint   string      `json:"endpoint,omitempty"`
	AuthMode   string      `json:"authMode"`
	Settings   Settings    `json:"settings"`
	Operations []Operation `json:"operations"`
	Health     string      `json:"health"`
	Revision   int         `json:"revision"`
	Enabled    bool        `json:"enabled"`
	HasSecret  bool        `json:"hasCredential"`
}

type ReadRequest struct {
	Operation string         `json:"operation,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	SQL       string         `json:"sql,omitempty"`
	Limit     int            `json:"limit,omitempty"`
}

type ReadResult struct {
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows"`
}

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	operationPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	selectorPattern   = regexp.MustCompile(`^table(?:[.#][a-zA-Z_][a-zA-Z0-9_-]{0,63})?$`)
)

func validID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

func validText(raw string, maximum int) bool {
	return raw != "" && utf8.ValidString(raw) && len(raw) <= maximum && !strings.ContainsFunc(raw, unicode.IsControl)
}

func endpointURL(raw string, sql bool) (*url.URL, error) {
	value, err := url.Parse(raw)
	if err != nil || len(raw) > 400 || value.User != nil || value.Fragment != "" || value.RawQuery != "" || value.ForceQuery || value.Hostname() == "" ||
		(value.Scheme != "https" && !(sql && (value.Scheme == "postgresql" || value.Scheme == "mysql"))) || (value.Path != "" && value.Path != "/") {
		return nil, ErrBadQuery
	}
	return value, nil
}

func approvedPath(raw string, parameterized bool) bool {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || len(raw) > 350 {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(raw, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		if parameterized && strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && identifierPattern.MatchString(part[1:len(part)-1]) {
			continue
		}
		if strings.ContainsFunc(part, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_-.", r))
		}) {
			return false
		}
	}
	return true
}

func normalizeLimit(value int) int {
	if value <= 0 {
		return 50
	}
	return min(value, maxBusinessRows)
}

func credentialScope(id string) string { return "business-connector/" + id }

func (c Credentials) valid() bool {
	for _, value := range []string{c.Token, c.Username, c.Password, c.AppKey, c.AppSecret, c.Account} {
		if value != "" && !validText(value, 4096) {
			return false
		}
	}
	return true
}

func scalar(kind string, value any) bool {
	switch kind {
	case "string":
		s, ok := value.(string)
		return ok && validText(s, 4096)
	case "number", "integer":
		switch n := value.(type) {
		case json.Number:
			if kind == "integer" {
				_, err := n.Int64()
				return err == nil
			}
			_, err := n.Float64()
			return err == nil
		case float64:
			return kind != "integer" || n == float64(int64(n))
		case int, int64:
			return true
		}
	case "boolean":
		_, ok := value.(bool)
		return ok
	}
	return false
}
