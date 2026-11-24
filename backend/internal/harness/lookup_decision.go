package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type LookupDecision struct {
	Answer     string
	References []int
	Question   string
	Terms      []string
}

var ErrLookupDecision = errors.New("invalid lookup decision")
var ErrLookupReferences = errors.New("invalid lookup references")

// DecodeLookup accepts one complete decision for the read-only lookup step.
// References are one-based positions in the evidence supplied to this turn.
func DecodeLookup(reply string, offered int) (LookupDecision, error) {
	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "```json\n") && strings.HasSuffix(reply, "\n```") {
		reply = strings.TrimSuffix(strings.TrimPrefix(reply, "```json\n"), "\n```")
	}
	dec := json.NewDecoder(strings.NewReader(reply))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return LookupDecision{}, ErrLookupDecision
	}
	var result LookupDecision
	seen := make(map[string]bool, 2)
	for dec.More() {
		token, err := dec.Token()
		key, isString := token.(string)
		if err != nil || !isString || seen[key] {
			return LookupDecision{}, ErrLookupDecision
		}
		seen[key] = true
		var target any
		switch key {
		case "answer":
			target = &result.Answer
		case "references":
			target = &result.References
		case "question":
			target = &result.Question
		case "terms":
			target = &result.Terms
		default:
			return LookupDecision{}, ErrLookupDecision
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) || json.Unmarshal(value, target) != nil {
			return LookupDecision{}, ErrLookupDecision
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return LookupDecision{}, ErrLookupDecision
	}
	if _, err := dec.Token(); err != io.EOF {
		return LookupDecision{}, ErrLookupDecision
	}
	result.Answer = strings.TrimSpace(result.Answer)
	result.Question = strings.TrimSpace(result.Question)
	switch {
	case len(seen) == 1 && seen["terms"] && len(result.Terms) > 0 && len(result.Terms) <= 6:
		return result, nil
	case len(seen) == 1 && seen["question"] && result.Question != "":
		return result, nil
	case len(seen) == 2 && seen["answer"] && seen["references"] && result.Answer != "" && len(result.References) > 0:
		referenced := make(map[int]bool, offered)
		for _, index := range result.References {
			if index <= 0 || index > offered || referenced[index] {
				return LookupDecision{}, ErrLookupReferences
			}
			referenced[index] = true
		}
		return result, nil
	default:
		return LookupDecision{}, ErrLookupDecision
	}
}
