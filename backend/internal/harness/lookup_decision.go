package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

type LookupDecision struct {
	Answer     string
	References []int
	Question   string
	Terms      []string
}

// IsReceiptQuestion bounds the visible input request separately from the
// existing answer contract.
func IsReceiptQuestion(question string) bool {
	return boundedEvidenceLine(question, 320)
}

func boundedEvidenceLine(text string, limit int) bool {
	size := utf8.RuneCountInString(text)
	return size > 0 && size <= limit && !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
	})
}

var ErrLookupDecision = errors.New("invalid lookup decision")
var ErrLookupReferences = errors.New("invalid lookup references")

const MaxLookupBatches = 3
const maxLookupTerms = 6

func decodeDecisionObject(reply string, fields map[string]any) (map[string]bool, error) {
	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "```json\n") && strings.HasSuffix(reply, "\n```") {
		reply = strings.TrimSuffix(strings.TrimPrefix(reply, "```json\n"), "\n```")
	}
	dec := json.NewDecoder(strings.NewReader(reply))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrLookupDecision
	}
	seen := make(map[string]bool, len(fields))
	for dec.More() {
		token, err := dec.Token()
		key, isString := token.(string)
		if err != nil || !isString || seen[key] {
			return nil, ErrLookupDecision
		}
		seen[key] = true
		target, allowed := fields[key]
		if !allowed {
			return nil, ErrLookupDecision
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) || json.Unmarshal(value, target) != nil {
			return nil, ErrLookupDecision
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, ErrLookupDecision
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrLookupDecision
	}
	return seen, nil
}

// DecodeLookup accepts one complete decision. References are one-based
// positions in the evidence supplied to this turn.
func DecodeLookup(reply string, offered int) (LookupDecision, error) {
	var result LookupDecision
	seen, err := decodeDecisionObject(reply, map[string]any{
		"answer": &result.Answer, "references": &result.References, "question": &result.Question, "terms": &result.Terms,
	})
	if err != nil {
		return LookupDecision{}, ErrLookupDecision
	}
	result.Answer = strings.TrimSpace(result.Answer)
	result.Question = strings.TrimSpace(result.Question)
	switch {
	case len(seen) == 1 && seen["terms"] && len(result.Terms) > 0 && len(result.Terms) <= maxLookupTerms:
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

// DecodeLookupBatches accepts distinct, bounded groups of read-only lookup
// terms. It never derives an action from a partial or mixed reply.
func DecodeLookupBatches(reply string) ([][]string, error) {
	var batches [][]string
	seen, err := decodeDecisionObject(reply, map[string]any{"searchBatches": &batches})
	if err != nil || len(seen) != 1 || len(batches) < 2 || len(batches) > MaxLookupBatches {
		return nil, ErrLookupDecision
	}
	terms := make(map[string]bool, maxLookupTerms)
	for _, batch := range batches {
		if len(batch) == 0 || len(terms)+len(batch) > maxLookupTerms {
			return nil, ErrLookupDecision
		}
		for i, term := range batch {
			term = strings.ToLower(strings.TrimSpace(term))
			if n := len([]rune(term)); n == 0 || n > 48 || terms[term] || strings.ContainsAny(term, "\r\n") {
				return nil, ErrLookupDecision
			}
			terms[term] = true
			batch[i] = term
		}
	}
	return batches, nil
}

type CalculationDecision struct {
	Operation string
	Operands  []int
}

func (d CalculationDecision) Valid(offered int) bool {
	if (d.Operation != "sum" && d.Operation != "difference") || len(d.Operands) < 2 || len(d.Operands) > 6 ||
		(d.Operation == "difference" && len(d.Operands) != 2) {
		return false
	}
	seen := make(map[int]bool, len(d.Operands))
	for _, id := range d.Operands {
		if id < 1 || id > offered || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// DecodeCalculation accepts a single operation over numbered input evidence.
// The model cannot supply currency, numeric values, or tool parameters.
func DecodeCalculation(reply string, offered int) (CalculationDecision, error) {
	var action json.RawMessage
	var result CalculationDecision
	seen, err := decodeDecisionObject(reply, map[string]any{"calculation": &action})
	if err != nil || len(seen) != 1 {
		return result, ErrLookupDecision
	}
	seen, err = decodeDecisionObject(string(action), map[string]any{"operation": &result.Operation, "operands": &result.Operands})
	if err != nil || len(seen) != 2 || !result.Valid(offered) {
		return CalculationDecision{}, ErrLookupDecision
	}
	return result, nil
}
