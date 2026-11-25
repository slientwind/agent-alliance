package evalsuite

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type Number struct {
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Tolerance float64 `json:"tolerance"`
}

type Assertions struct {
	Facts     []string `json:"facts,omitempty"`
	Citations []string `json:"citations,omitempty"`
	Numbers   []Number `json:"numbers,omitempty"`
}

type modelEvaluation struct {
	Answer    string             `json:"answer"`
	Citations []string           `json:"citations"`
	Numbers   map[string]float64 `json:"numbers"`
}

type Outcome struct {
	Passed     bool     `json:"passed"`
	LatencyMs  int64    `json:"latencyMs"`
	Model      string   `json:"modelId,omitempty"`
	Deviations []string `json:"deviations"`
}

var numberLine = regexp.MustCompile(`(?m)^[-* ]*([\p{Han}A-Za-z_][^\n:：]{0,26})[:：][ ]*(-?[0-9]+(?:\.[0-9]+)?)(?:%|元|件|条|单|天|人)?(?:[ 。]|$)`)
var citationID = regexp.MustCompile(`^K-[0-9a-f]{16}$`)

func capturedAssertions(answer string, citations []string) Assertions {
	out := Assertions{Citations: citations}
	for _, match := range numberLine.FindAllStringSubmatch(answer, 8) {
		key := strings.TrimSpace(match[1])
		n, err := strconv.ParseFloat(match[2], 64)
		if err != nil || !finite(n) || slices.ContainsFunc(out.Numbers, func(item Number) bool { return item.Key == key }) {
			continue
		}
		out.Numbers = append(out.Numbers, Number{Key: key, Value: n, Tolerance: math.Max(0.000001, math.Abs(n)*0.001)})
	}
	if len(out.Numbers) == 0 {
		for _, line := range strings.Split(answer, "\n") {
			fact := strings.Trim(line, " \t#*-。")
			if utf8.RuneCountInString(fact) >= 6 && utf8.RuneCountInString(fact) <= 85 && !strings.Contains(fact, "[K-") {
				out.Facts = append(out.Facts, fact)
				if len(out.Facts) == 2 {
					break
				}
			}
		}
	}
	return out
}

func normFact(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(text)), " "))
}

func finite(n float64) bool { return !math.IsInf(n, 0) && !math.IsNaN(n) }

func validateAssertions(a Assertions) bool {
	if len(a.Facts)+len(a.Citations)+len(a.Numbers) == 0 || len(a.Facts) > 12 || len(a.Citations) > 12 || len(a.Numbers) > 12 {
		return false
	}
	seen := map[string]bool{}
	for _, fact := range a.Facts {
		if !validText(fact, 360) {
			return false
		}
	}
	for _, ref := range a.Citations {
		if !citationID.MatchString(ref) {
			return false
		}
	}
	for _, check := range a.Numbers {
		if !validText(check.Key, 120) || seen[check.Key] || !finite(check.Value) || !finite(check.Tolerance) ||
			check.Tolerance < 0 || check.Tolerance > math.Max(1, math.Abs(check.Value)) {
			return false
		}
		seen[check.Key] = true
	}
	return true
}

func evaluateAssertions(raw string, checks Assertions, authorizedCitations []string) []string {
	if len(raw) > 32<<10 {
		return []string{"invalid_output"}
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json\n") && strings.HasSuffix(raw, "\n```") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "```json\n"), "\n```")
	}
	var result modelEvaluation
	if json.Unmarshal([]byte(raw), &result) != nil || strings.TrimSpace(result.Answer) == "" {
		return []string{"invalid_output"}
	}
	problems := []string{}
	for i, fact := range checks.Facts {
		if !strings.Contains(normFact(result.Answer), normFact(fact)) {
			problems = append(problems, "fact:"+strconv.Itoa(i+1))
		}
	}
	for _, ref := range checks.Citations {
		if !slices.Contains(result.Citations, ref) || !slices.Contains(authorizedCitations, ref) {
			problems = append(problems, "citation:"+ref)
		}
	}
	for _, ref := range result.Citations {
		if !slices.Contains(authorizedCitations, ref) {
			problems = append(problems, "unverified_citation")
			break
		}
	}
	for i, check := range checks.Numbers {
		value, ok := result.Numbers[check.Key]
		if !ok || !finite(value) || math.Abs(value-check.Value) > check.Tolerance+math.Abs(check.Value)*1e-12 {
			problems = append(problems, "number:"+strconv.Itoa(i+1))
		}
	}
	return problems
}
