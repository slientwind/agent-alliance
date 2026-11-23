package runtime

import (
	"math/bits"
	"strings"
	"unicode"

	"alliance/internal/knowledge"
)

type lookupTerm struct {
	text  string
	pairs []string
}

type sourceSentences struct {
	runes   []rune
	clauses []knowledge.Sentence
}

// Use character pairs only for Han phrases that have no literal match. Names,
// numbers, file identifiers and short terms keep their original matching rules.
func prepareLookup(terms []string, files []knowledge.InputSource) ([]lookupTerm, []sourceSentences, error) {
	queries := make([]lookupTerm, len(terms))
	for i, s := range terms {
		queries[i].text = s
		chars := []rune(s)
		if len(chars) < 4 || strings.IndexFunc(s, func(r rune) bool { return !unicode.Is(unicode.Han, r) }) >= 0 {
			continue
		}
		seen := map[string]bool{}
		for k := 1; k < len(chars); k++ {
			pair := string(chars[k-1 : k+1])
			if !seen[pair] {
				queries[i].pairs = append(queries[i].pairs, pair)
				seen[pair] = true
			}
		}
	}
	inputs := make([]sourceSentences, 0, len(files))
	for _, file := range files {
		rows, err := knowledge.LocatedSentences(file.Text, file.Locations)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			for i, t := range queries {
				if len(t.pairs) > 0 && strings.Contains(row.Text, t.text) {
					queries[i].pairs = nil
				}
			}
		}
		inputs = append(inputs, sourceSentences{[]rune(file.Text), rows})
	}
	return queries, inputs, nil
}

type termMatch struct {
	position int
	score    int
	matched  uint8
	strong   uint8
}

func matchLookup(text string, terms []lookupTerm) termMatch {
	text = strings.ToLower(text)
	m := termMatch{position: -1}
	best := 0
	for i, t := range terms {
		position := strings.Index(text, t.text)
		exact := position >= 0
		weight := 0
		if exact {
			weight = 64
		} else {
			for _, pair := range t.pairs {
				at := strings.Index(text, pair)
				if at < 0 {
					continue
				}
				if position < 0 || at < position {
					position = at
				}
				weight++
			}
		}
		if weight == 0 {
			continue
		}
		flag := uint8(1) << i
		m.matched |= flag
		if weight >= 2 {
			m.strong |= flag
		}
		if !exact {
			weight = min(weight, 6)
		}
		m.score += weight
		if weight > best || weight == best && position < m.position {
			best = weight
			m.position = position
		}
	}
	return m
}

func lookupCandidates(terms []lookupTerm, inputs []sourceSentences, files []knowledge.InputSource) []lookupHit {
	var hits []lookupHit
	for i, src := range inputs {
		type candidate struct {
			hit   lookupHit
			match termMatch
		}
		var found []candidate
		var strong uint8
		for _, clause := range src.clauses {
			m := matchLookup(clause.Text, terms)
			if m.position < 0 {
				continue
			}
			start, end := clause.Start-1, clause.End
			anchor := start + len([]rune(strings.ToLower(clause.Text)[:m.position]))
			left := max(start, anchor-96)
			right := min(end, left+320)
			for left < right && unicode.IsSpace(src.runes[left]) {
				left++
			}
			for right > left && unicode.IsSpace(src.runes[right-1]) {
				right--
			}
			quote := string(src.runes[left:right])
			if left != start || right != end {
				m = matchLookup(quote, terms)
			}
			if m.score == 0 {
				continue
			}
			found = append(found, candidate{lookupHit{i, files[i].Version, left, right, m.score, quote}, m})
			strong |= m.strong
		}
		for _, c := range found {
			// One matching pair alone is weak. Include it only when a different
			// term anchors this source, or two terms independently match this excerpt.
			if c.match.strong != 0 || bits.OnesCount8(c.match.matched) >= 2 || strong&^c.match.matched != 0 {
				hits = append(hits, c.hit)
			}
		}
	}
	return hits
}
