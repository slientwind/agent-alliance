package harness

import (
	"encoding/json"
	"errors"
)

var (
	ErrContextCapacity = errors.New("required context exceeds the model input allowance")
	ErrContextHistory  = errors.New("tool calls and results do not form a valid history")
)

// ContextEntry contains metadata about a message whose content and native
// provider fields are kept by the caller. Bytes includes its serialized form.
type ContextEntry struct {
	Role       string
	Bytes      int
	Correction bool
	CallIDs    []string
	ResultFor  string
	Sources    []SourceRevision
}

type ContextSelection struct {
	Indices []int
	Sources []SourceRevision
}

type contextGroup struct {
	indices []int
	calls   int
	bytes   int
	keep    bool
}

// SelectLoopContext retains system instructions, the newest user request and
// correction, source revision identifiers, and every partially answered tool
// group. Older complete tool groups are kept or dropped as a unit.
func SelectLoopContext(history []ContextEntry, byteLimit int) (ContextSelection, error) {
	groups := make([]contextGroup, 0, len(history))
	callGroup := make(map[string]int)
	latestUser, latestCorrection := -1, -1
	var refs []SourceRevision
	for i, entry := range history {
		if entry.Bytes < 0 {
			return ContextSelection{}, ErrContextHistory
		}
		for _, ref := range entry.Sources {
			if ref.ID == "" || ref.Revision <= 0 {
				return ContextSelection{}, ErrContextHistory
			}
			refs = append(refs, ref)
		}
		if entry.Role == "tool" {
			index, exists := callGroup[entry.ResultFor]
			if !exists || index < 0 || len(entry.CallIDs) != 0 {
				return ContextSelection{}, ErrContextHistory
			}
			callGroup[entry.ResultFor] = -1
			group := &groups[index]
			group.indices = append(group.indices, i)
			group.bytes += entry.Bytes + 1
			group.calls--
			continue
		}
		if (entry.Role != "system" && entry.Role != "user" && entry.Role != "assistant") || entry.ResultFor != "" ||
			(entry.Role != "assistant" && len(entry.CallIDs) != 0) || (entry.Correction && entry.Role != "user") {
			return ContextSelection{}, ErrContextHistory
		}
		groupIndex := len(groups)
		if entry.Role == "user" {
			latestUser = groupIndex
			if entry.Correction {
				latestCorrection = groupIndex
			}
		}
		for _, id := range entry.CallIDs {
			if _, exists := callGroup[id]; exists || id == "" {
				return ContextSelection{}, ErrContextHistory
			}
			callGroup[id] = groupIndex
		}
		groups = append(groups, contextGroup{indices: []int{i}, calls: len(entry.CallIDs), bytes: entry.Bytes + 1, keep: entry.Role == "system"})
	}
	return allocateLoopContext(groups, sortedSourceRevisions(refs), latestUser, latestCorrection, byteLimit, len(history))
}

func allocateLoopContext(groups []contextGroup, refs []SourceRevision, user, correction, limit, size int) (ContextSelection, error) {
	selected := make([]bool, size)
	remaining := limit - 2 // JSON message array brackets
	if len(refs) > 0 {
		encoded, err := json.Marshal(refs)
		if err != nil {
			return ContextSelection{}, err
		}
		remaining -= len(encoded)
	}
	for i := range groups {
		g := &groups[i]
		if g.keep || g.calls > 0 || i == user || i == correction {
			g.keep = true
			remaining -= g.bytes
			if remaining < 0 {
				return ContextSelection{}, ErrContextCapacity
			}
		}
	}
	if remaining < 0 {
		return ContextSelection{}, ErrContextCapacity
	}
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		if !g.keep {
			if g.bytes > remaining {
				continue
			}
			remaining -= g.bytes
		}
		for _, index := range g.indices {
			selected[index] = true
		}
	}
	out := ContextSelection{Sources: refs}
	for index, keep := range selected {
		if keep {
			out.Indices = append(out.Indices, index)
		}
	}
	return out, nil
}
