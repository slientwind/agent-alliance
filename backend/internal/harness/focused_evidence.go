package harness

import (
	"encoding/json"
	"strings"
)

const MaxEvidenceActions = 3

type EvidenceAction struct {
	Source    int      `json:"source,omitempty"`
	Terms     []string `json:"terms,omitempty"`
	Reference int      `json:"reference,omitempty"`
	Side      string   `json:"side,omitempty"`
}

// DecodeEvidenceActions authorizes the whole decision against the catalog
// shown to the model. The caller still reopens and checks the pinned inputs.
func DecodeEvidenceActions(reply string, sources, references []bool) ([]EvidenceAction, error) {
	var objects []json.RawMessage
	seen, err := decodeDecisionObject(reply, map[string]any{"inspectEvidence": &objects})
	if err != nil || len(seen) != 1 || len(objects) == 0 || len(objects) > MaxEvidenceActions {
		return nil, ErrLookupDecision
	}
	var actions []EvidenceAction
	terms := make(map[int]map[string]bool)
	neighbors := make(map[struct {
		reference int
		side      string
	}]bool)
	totalTerms := 0
	for _, object := range objects {
		var action EvidenceAction
		fields, err := decodeDecisionObject(string(object), map[string]any{
			"source": &action.Source, "terms": &action.Terms, "reference": &action.Reference, "side": &action.Side,
		})
		if err != nil || len(fields) != 2 {
			return nil, ErrLookupDecision
		}
		switch {
		case fields["source"] && fields["terms"]:
			if !offeredEvidenceSlot(sources, action.Source) || len(action.Terms) == 0 || totalTerms+len(action.Terms) > maxLookupTerms {
				return nil, ErrLookupDecision
			}
			if terms[action.Source] == nil {
				terms[action.Source] = make(map[string]bool)
			}
			for i, word := range action.Terms {
				word = strings.ToLower(strings.TrimSpace(word))
				if !boundedEvidenceLine(word, 48) || terms[action.Source][word] {
					return nil, ErrLookupDecision
				}
				terms[action.Source][word] = true
				action.Terms[i] = word
			}
			totalTerms += len(action.Terms)
		case fields["reference"] && fields["side"]:
			key := struct {
				reference int
				side      string
			}{action.Reference, action.Side}
			if !offeredEvidenceSlot(references, action.Reference) || (action.Side != "before" && action.Side != "after") || neighbors[key] {
				return nil, ErrLookupDecision
			}
			neighbors[key] = true
		default:
			return nil, ErrLookupDecision
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func offeredEvidenceSlot(slots []bool, index int) bool {
	return index >= 1 && index <= len(slots) && slots[index-1]
}
