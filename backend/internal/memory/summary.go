package memory

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Summarize selects complete sentences carrying decisions, identifiers, values,
// or constraints. It retains the start and ending update when space permits.
func Summarize(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	if limit < 4 {
		return ""
	}
	var sentences []string
	start := 0
	for i, r := range text {
		if strings.ContainsRune("。！？.!?\n；;", r) &&
			!(r == '.' && i > 0 && i+1 < len(text) && text[i-1] >= '0' && text[i-1] <= '9' && text[i+1] >= '0' && text[i+1] <= '9') {
			end := i + utf8.RuneLen(r)
			if sentence := strings.TrimSpace(text[start:end]); sentence != "" {
				sentences = append(sentences, sentence)
			}
			start = end
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		sentences = append(sentences, tail)
	}
	if len(sentences) == 0 {
		return ""
	}
	type rank struct{ index, weight int }
	ranked := make([]rank, 0, len(sentences))
	for i, value := range sentences {
		score := 0
		if i == 0 || i == len(sentences)-1 {
			score += 2
		}
		if strings.ContainsFunc(value, unicode.IsDigit) {
			score += 2
		}
		for _, word := range []string{"请", "改为", "更正", "已经", "结论", "需要", "禁止", "截止", "待确认", "should", "instead", "approved", "due"} {
			if strings.Contains(strings.ToLower(value), word) {
				score += 2
				break
			}
		}
		ranked = append(ranked, rank{i, score})
	}
	slices.SortStableFunc(ranked, func(left, right rank) int { return right.weight - left.weight })
	var selected []int
	seen := map[string]bool{}
	for _, candidate := range ranked {
		value := sentences[candidate.index]
		if len(value)+3 > limit || seen[value] {
			continue
		}
		seen[value] = true
		selected = append(selected, candidate.index)
		limit -= len(value) + 3
	}
	if len(selected) == 0 {
		limit -= 3
		for limit > 0 && !utf8.RuneStart(text[limit]) {
			limit--
		}
		return text[:limit] + "…"
	}
	slices.Sort(selected)
	var parts []string
	for _, i := range selected {
		parts = append(parts, sentences[i])
	}
	return strings.Join(parts, " ")
}
