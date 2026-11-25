package policy

import (
	"bytes"
	"encoding/json"
	"errors"

	"alliance/internal/web"
)

// JSONFields rejects credential-bearing arguments before a connector receives
// them. Changing a certified tool call after approval would invalidate the
// approved request, so the caller must request corrected arguments.
func JSONFields(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 32<<10 || !json.Valid(raw) {
		return errors.New("invalid connector arguments")
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil || data == nil {
		return errors.New("invalid connector arguments")
	}
	if containsCredential(data) {
		return ErrDenied
	}
	return nil
}

func containsCredential(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if sensitiveField(key) || containsCredential(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsCredential(child) {
				return true
			}
		}
	case string:
		return web.TraceText(value) != value
	}
	return false
}
