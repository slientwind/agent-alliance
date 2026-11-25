package resources

import (
	"encoding/json"
	"regexp"
	"strconv"
)

var pathSelector = regexp.MustCompile(`^\.([A-Za-z_][A-Za-z0-9_-]*)|^\[(0|[1-9][0-9]{0,4})\]|^\[("(?:[^"\\]|\\.)*")\]|^\['([^'\\]*)'\]`)

type jsonPathStep struct {
	member string
	index  *int
}

// compileJSONPath accepts singular object and array selectors so a receipt is
// always bound to one payload field rather than a wildcard match.
func compileJSONPath(expression string) ([]jsonPathStep, bool) {
	if expression == "" || expression[0] != '$' || len(expression) > 240 {
		return nil, false
	}
	expression = expression[1:]
	var steps []jsonPathStep
	for expression != "" {
		selector := pathSelector.FindStringSubmatch(expression)
		if selector == nil || len(steps) == 16 {
			return nil, false
		}
		step := jsonPathStep{member: selector[1]}
		if selector[2] != "" {
			index, err := strconv.Atoi(selector[2])
			if err != nil {
				return nil, false
			}
			step.index = &index
		} else if selector[3] != "" {
			if json.Unmarshal([]byte(selector[3]), &step.member) != nil {
				return nil, false
			}
		} else if selector[1] == "" {
			step.member = selector[4]
		}
		steps = append(steps, step)
		expression = expression[len(selector[0]):]
	}
	return steps, true
}

func readJSONPath(input json.RawMessage, expression string) json.RawMessage {
	steps, ok := compileJSONPath(expression)
	if !ok {
		return nil
	}
	for _, step := range steps {
		if step.index != nil {
			var entries []json.RawMessage
			if json.Unmarshal(input, &entries) != nil || *step.index >= len(entries) {
				return nil
			}
			input = entries[*step.index]
		} else {
			var entries map[string]json.RawMessage
			if json.Unmarshal(input, &entries) != nil {
				return nil
			}
			input = entries[step.member]
		}
	}
	return input
}

func pathString(raw json.RawMessage, path string) string {
	var value string
	_ = json.Unmarshal(readJSONPath(raw, path), &value)
	return value
}
