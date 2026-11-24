package harness

import (
	"encoding/json"
	"strings"
)

type RequirementEvidenceItem struct {
	Item       int
	Answer     string
	References []int
	Gap        string
}

type RequirementEvidenceReply struct {
	Answer     string
	References []int
	Items      []RequirementEvidenceItem
}

// DecodeRequirementEvidence accepts only the item IDs and reference positions
// offered to the model. Omitted items remain available for manual review.
func DecodeRequirementEvidence(raw string, items map[int]string, offered int) (RequirementEvidenceReply, error) {
	var result RequirementEvidenceReply
	var entries []json.RawMessage
	seen, err := decodeDecisionObject(raw, map[string]any{
		"items": &entries, "answer": &result.Answer, "references": &result.References,
	})
	if err != nil || !seen["items"] || len(entries) == 0 || len(entries) > len(items) {
		return RequirementEvidenceReply{}, ErrLookupDecision
	}
	result.Answer = strings.TrimSpace(result.Answer)
	if seen["answer"] != seen["references"] || seen["answer"] &&
		(result.Answer == "" || !validLookupReferences(result.References, offered)) {
		return RequirementEvidenceReply{}, ErrLookupDecision
	}
	used := make(map[int]bool, len(entries))
	for _, entry := range entries {
		var item RequirementEvidenceItem
		fields, err := decodeDecisionObject(string(entry), map[string]any{
			"item": &item.Item, "answer": &item.Answer, "references": &item.References, "gap": &item.Gap,
		})
		if _, allowed := items[item.Item]; err != nil || !fields["item"] || !allowed || used[item.Item] {
			return RequirementEvidenceReply{}, ErrLookupDecision
		}
		item.Answer, item.Gap = strings.TrimSpace(item.Answer), strings.TrimSpace(item.Gap)
		switch {
		case len(fields) == 2 && fields["gap"] && boundedEvidenceLine(item.Gap, 320):
		case len(fields) == 3 && fields["answer"] && fields["references"] && boundedEvidenceLine(item.Answer, 600) &&
			validLookupReferences(item.References, offered):
		default:
			return RequirementEvidenceReply{}, ErrLookupDecision
		}
		used[item.Item] = true
		result.Items = append(result.Items, item)
	}
	return result, nil
}
