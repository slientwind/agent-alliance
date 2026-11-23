package knowledge

import (
	"alliance/internal/web"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Sentence struct {
	Text     string `json:"text"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Location string `json:"location,omitempty"`
}

// Positions are one-based Unicode character positions, as in project retrieval.
func Sentences(text string) []Sentence {
	runes := []rune(text)
	var rows []Sentence
	for start, i := 0, 0; i < len(runes); i++ {
		c := runes[i]
		lineEnd := c == '\n' || c == '\r'
		fullStop := c == '.' && (i+1 == len(runes) || unicode.IsSpace(runes[i+1]))
		if !lineEnd && !fullStop && !strings.ContainsRune("。！？；!?;", c) && i+1 < len(runes) {
			continue
		}
		end := i + 1
		if lineEnd {
			end--
		}
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		fragment := string(runes[start:end])
		if strings.IndexFunc(fragment, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r) }) >= 0 {
			rows = append(rows, Sentence{Text: fragment, Start: start + 1, End: end})
		}
		start = i + 1
	}
	return rows
}

// The Office reader embeds location labels in its search text. Preserve absolute
// character positions but compare only the content inside each recorded segment.
func LocatedSentences(text string, encoded []byte) ([]Sentence, error) {
	var locations []textLocation
	if len(encoded) == 0 {
		return Sentences(text), nil
	}
	if err := json.Unmarshal(encoded, &locations); err != nil {
		return nil, err
	}
	if len(locations) == 0 {
		return Sentences(text), nil
	}
	runes := []rune(text)
	var rows []Sentence
	prev := 0
	for _, loc := range locations {
		start := loc.Start - 1
		if start < prev || loc.End > len(runes) || start >= loc.End {
			return nil, web.Fail(409, "资料段落位置需要重新核对")
		}
		segment := string(runes[start:loc.End])
		prefix := loc.Label + " · "
		content, ok := strings.CutPrefix(segment, prefix)
		if !ok {
			return nil, web.Fail(409, "资料段落位置需要重新核对")
		}
		offset := start + utf8.RuneCountInString(prefix)
		for _, row := range Sentences(content) {
			row.Start += offset
			row.End += offset
			row.Location = loc.Label
			rows = append(rows, row)
		}
		prev = loc.End
	}
	return rows, nil
}
