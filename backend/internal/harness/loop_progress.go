package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
)

type SourceRevision struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

type ActionFingerprint struct {
	Tool       string
	Arguments  json.RawMessage
	Sources    []SourceRevision
	Unresolved []string
}

var ErrToolArguments = errors.New("invalid arguments for a tool call")

func (a ActionFingerprint) Digest() (string, error) {
	if strings.TrimSpace(a.Tool) == "" {
		return "", ErrToolArguments
	}
	args, err := CanonicalToolArguments(a.Arguments)
	if err != nil {
		return "", err
	}
	sources := sortedSourceRevisions(a.Sources)
	for _, source := range sources {
		if source.ID == "" || source.Revision <= 0 {
			return "", ErrToolArguments
		}
	}
	unresolved := slices.Clone(a.Unresolved)
	slices.Sort(unresolved)
	encoded, err := json.Marshal(ActionFingerprint{a.Tool, args, sources, slices.Compact(unresolved)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sortedSourceRevisions(input []SourceRevision) []SourceRevision {
	refs := slices.Clone(input)
	slices.SortFunc(refs, func(a, b SourceRevision) int {
		if byID := strings.Compare(a.ID, b.ID); byID != 0 {
			return byID
		}
		if a.Revision < b.Revision {
			return -1
		}
		if a.Revision > b.Revision {
			return 1
		}
		return 0
	})
	return slices.Compact(refs)
}

type LoopProgress struct {
	Fingerprint       string
	Covered           int
	ConsecutiveStalls int
}

// BeforeAction stops the next repeated dispatch after two decisions on the
// same tool and context produced no additional durable observation coverage.
func (p *LoopProgress) BeforeAction(fingerprint string, covered int) bool {
	if covered > p.Covered || fingerprint != p.Fingerprint {
		p.Fingerprint, p.Covered, p.ConsecutiveStalls = fingerprint, covered, 0
		return false
	}
	p.ConsecutiveStalls = min(2, p.ConsecutiveStalls+1)
	return p.ConsecutiveStalls == 2
}

// CanonicalToolArguments normalizes object keys, whitespace and decimal
// spellings without converting large integers to floating point.
func CanonicalToolArguments(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, ErrToolArguments
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := canonicalArgument(d, 0)
	if _, ok := value.(map[string]any); err != nil || !ok {
		return nil, ErrToolArguments
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrToolArguments
	}
	return json.Marshal(value)
}

func canonicalArgument(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrToolArguments
	}
	token, err := d.Token()
	if err != nil {
		return nil, ErrToolArguments
	}
	switch token {
	case json.Delim('{'):
		out := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if _, duplicate := out[name]; err != nil || !ok || duplicate {
				return nil, ErrToolArguments
			}
			value, err := canonicalArgument(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		return finishArgument(d, '}', out)
	case json.Delim('['):
		out := []any{}
		for d.More() {
			value, err := canonicalArgument(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return finishArgument(d, ']', out)
	}
	if n, ok := token.(json.Number); ok {
		return canonicalNumber(n.String())
	}
	return token, nil
}

func finishArgument(d *json.Decoder, closing json.Delim, out any) (any, error) {
	if token, err := d.Token(); err != nil || token != closing {
		return nil, ErrToolArguments
	}
	return out, nil
}

func canonicalNumber(raw string) (json.Number, error) {
	sign := ""
	if raw[0] == '-' {
		sign, raw = "-", raw[1:]
	}
	exponent := int64(0)
	if at := strings.IndexAny(raw, "eE"); at >= 0 {
		var err error
		exponent, err = strconv.ParseInt(raw[at+1:], 10, 32)
		if err != nil {
			return "", ErrToolArguments
		}
		raw = raw[:at]
	}
	if whole, fraction, ok := strings.Cut(raw, "."); ok {
		exponent -= int64(len(fraction))
		raw = whole + fraction
	}
	coefficient := strings.TrimLeft(raw, "0")
	if coefficient == "" {
		return "0", nil
	}
	short := strings.TrimRight(coefficient, "0")
	exponent += int64(len(coefficient) - len(short))
	if exponent >= 0 && exponent <= 32 {
		return json.Number(sign + short + strings.Repeat("0", int(exponent))), nil
	}
	return json.Number(sign + short + "e" + strconv.FormatInt(exponent, 10)), nil
}
