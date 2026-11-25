package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"alliance/internal/capability"
	"alliance/internal/llmgateway"
)

type Spec struct {
	Name         string          `json:"name"`
	Purpose      string          `json:"purpose"`
	Instructions string          `json:"instructions"`
	Steps        []string        `json:"steps"`
	Checks       []string        `json:"checks"`
	Capabilities []string        `json:"capabilities"`
	Tier         llmgateway.Tier `json:"tier"`
}

type Patch struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type suggestion struct {
	Reply     string   `json:"reply"`
	Questions []string `json:"questions"`
	Patch     []Patch  `json:"patch"`
}

type Turn struct {
	Revision int  `json:"revision"`
	Spec     Spec `json:"spec"`
	suggestion
}

var errPatch = errors.New("malformed studio patch")

func emptySpec() Spec {
	return Spec{Steps: []string{}, Checks: []string{}, Capabilities: []string{}, Tier: llmgateway.Default}
}

func decodeExact(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errPatch
	}
	return nil
}

func unwrapJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if fenced, ok := strings.CutPrefix(raw, "```json\n"); ok {
		if body, ok := strings.CutSuffix(fenced, "\n```"); ok {
			return body
		}
	}
	return raw
}

func modelSuggestion(raw string, current Spec) (suggestion, Spec, error) {
	raw = unwrapJSON(raw)
	var out suggestion
	if len(raw) > 48<<10 || decodeExact([]byte(raw), &out) != nil || !validText(out.Reply, 1200, false) ||
		!validList(out.Questions, 2, 250) || len(out.Patch) > 7 || len(out.Patch)+len(out.Questions) == 0 {
		return suggestion{}, Spec{}, errPatch
	}
	changed, err := patchSpec(current, out.Patch)
	return out, changed, err
}

func patchSpec(spec Spec, patch []Patch) (Spec, error) {
	spec.Steps = slices.Clone(spec.Steps)
	spec.Checks = slices.Clone(spec.Checks)
	spec.Capabilities = slices.Clone(spec.Capabilities)
	seen := make(map[string]bool)
	for _, change := range patch {
		if change.Op != "replace" || seen[change.Path] || len(change.Value) == 0 || bytes.Equal(bytes.TrimSpace(change.Value), []byte("null")) {
			return Spec{}, errPatch
		}
		seen[change.Path] = true
		var target any
		switch change.Path {
		case "/name":
			target = &spec.Name
		case "/purpose":
			target = &spec.Purpose
		case "/instructions":
			target = &spec.Instructions
		case "/steps":
			target = &spec.Steps
		case "/checks":
			target = &spec.Checks
		case "/capabilities":
			target = &spec.Capabilities
		case "/tier":
			target = &spec.Tier
		default:
			return Spec{}, errPatch
		}
		if decodeExact(change.Value, target) != nil {
			return Spec{}, errPatch
		}
	}
	if !validText(spec.Name, 120, true) || !validText(spec.Purpose, 800, true) ||
		!validText(spec.Instructions, 6000, true) || !validList(spec.Steps, 16, 500) || !validList(spec.Checks, 16, 500) {
		return Spec{}, errPatch
	}
	switch spec.Tier {
	case llmgateway.Default, llmgateway.Fast, llmgateway.Reasoning:
	default:
		return Spec{}, errPatch
	}
	if !capability.Valid(spec.Capabilities) {
		return Spec{}, errPatch
	}
	return spec, nil
}

func validText(value string, limit int, allowEmpty bool) bool {
	return (allowEmpty || strings.TrimSpace(value) != "") && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}

func validList(list []string, limit, size int) bool {
	if len(list) > limit {
		return false
	}
	for _, item := range list {
		if !validText(item, size, false) {
			return false
		}
	}
	return true
}
