package policy

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Sensitivity string

const (
	SensitivityPublic       Sensitivity = "public"
	SensitivityInternal     Sensitivity = "internal"
	SensitivityConfidential Sensitivity = "confidential"
	SensitivitySecret       Sensitivity = "secret"
)

type DLPKind string

const (
	DLPMobile     DLPKind = "mobile"
	DLPEmail      DLPKind = "email"
	DLPNationalID DLPKind = "national_id"
	DLPPayment    DLPKind = "payment_card"
	DLPFloorPrice DLPKind = "floor_price"
	DLPCustom     DLPKind = "custom_term"
	DLPLabel      DLPKind = "classified_field"
)

type DLPRule struct {
	Kind        DLPKind
	Sensitivity Sensitivity
	Match       string
}

type DLPFinding struct {
	// Start and End identify a UTF-8 byte range in the original value.
	Start       int         `json:"start"`
	End         int         `json:"end"`
	Kind        DLPKind     `json:"kind"`
	Sensitivity Sensitivity `json:"sensitivity"`
}

const MaxDLPRules = 200
const DLPRuleLength = 120

type DLPScanner struct {
	custom []dlpPattern
	floor  map[string]Sensitivity
}

type dlpPattern struct {
	kind        DLPKind
	sensitivity Sensitivity
	regexp      *regexp.Regexp
}

var (
	dlpPhones         = regexp.MustCompile(`(?:\+86[- \t]?)?1[3-9][0-9](?:[- \t]?[0-9]{4}){2}`)
	dlpIDs            = regexp.MustCompile(`[1-9][0-9]{5}(?:19|20)[0-9]{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`)
	dlpDigitRuns      = regexp.MustCompile(`[0-9]+`)
	dlpEmails         = regexp.MustCompile("(?i)[a-z0-9.!#$%&'*+/=?^_\x60{|}~-]{1,64}@[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?)*\\.[a-z]{2,63}")
	ErrInvalidDLPRule = errors.New("invalid DLP rule")
)

func NewDLPScanner(rules []DLPRule) (DLPScanner, error) {
	scanner := DLPScanner{floor: make(map[string]Sensitivity)}
	if len(rules) > MaxDLPRules {
		return DLPScanner{}, ErrInvalidDLPRule
	}
	type bucket struct {
		kind        DLPKind
		sensitivity Sensitivity
	}
	groups := make(map[bucket][]string)
	for _, rule := range rules {
		rule.Match = strings.TrimSpace(rule.Match)
		if (rule.Kind != DLPFloorPrice && rule.Kind != DLPCustom) ||
			(rule.Sensitivity != SensitivityConfidential && rule.Sensitivity != SensitivitySecret) ||
			rule.Match == "" || !utf8.ValidString(rule.Match) || utf8.RuneCountInString(rule.Match) > DLPRuleLength ||
			strings.ContainsAny(rule.Match, "\x00\r\n") {
			return DLPScanner{}, ErrInvalidDLPRule
		}
		value, _ := dlpNormalized(rule.Match)
		key := bucket{rule.Kind, rule.Sensitivity}
		groups[key] = append(groups[key], value)
		if rule.Kind == DLPFloorPrice {
			name := strings.ToLower(value)
			if sensitivityRank(rule.Sensitivity) > sensitivityRank(scanner.floor[name]) {
				scanner.floor[name] = rule.Sensitivity
			}
		}
	}
	// A single RE2 pass per kind and sensitivity bounds the work for a long
	// field. Longest matching labels take precedence at the same offset.
	for _, key := range []bucket{
		{DLPCustom, SensitivitySecret}, {DLPCustom, SensitivityConfidential},
		{DLPFloorPrice, SensitivitySecret}, {DLPFloorPrice, SensitivityConfidential},
	} {
		terms := groups[key]
		if len(terms) == 0 {
			continue
		}
		slices.SortFunc(terms, func(a, b string) int {
			if len(a) != len(b) {
				return len(b) - len(a)
			}
			return strings.Compare(a, b)
		})
		for i, term := range terms {
			terms[i] = regexp.QuoteMeta(term)
		}
		pattern := "(?i)(?:" + strings.Join(terms, "|") + ")"
		if key.kind == DLPFloorPrice {
			pattern += `(?:[ \t]*[:：=为約约][ \t]*|[ \t]+)((?:[¥￥$][ \t]*)?[-+]?[0-9]+(?:[,，][0-9]{3})*(?:\.[0-9]+)?(?:[ \t]*(?:CNY|USD|RMB|元))?)`
		}
		rx, err := regexp.Compile(pattern)
		if err != nil {
			return DLPScanner{}, ErrInvalidDLPRule
		}
		scanner.custom = append(scanner.custom, dlpPattern{key.kind, key.sensitivity, rx})
	}
	return scanner, nil
}

func sensitivityRank(value Sensitivity) int {
	switch value {
	case SensitivityPublic:
		return 1
	case SensitivityInternal:
		return 2
	case SensitivityConfidential:
		return 3
	case SensitivitySecret:
		return 4
	}
	return 0
}

// Scan accepts a trusted source or view label. An explicit confidential or
// secret label applies to the whole field, including text without known patterns.
func (s DLPScanner) Scan(field, value string, label Sensitivity) []DLPFinding {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || (label != "" && sensitivityRank(label) == 0) {
		return []DLPFinding{{Start: 0, End: len(value), Kind: DLPLabel, Sensitivity: SensitivitySecret}}
	}
	field, _ = dlpNormalized(strings.TrimSpace(field))
	if floor := s.floor[strings.ToLower(field)]; sensitivityRank(floor) > sensitivityRank(label) {
		return []DLPFinding{{Start: 0, End: len(value), Kind: DLPFloorPrice, Sensitivity: floor}}
	}
	if sensitivityRank(label) >= sensitivityRank(SensitivityConfidential) {
		return []DLPFinding{{Start: 0, End: len(value), Kind: DLPLabel, Sensitivity: label}}
	}
	normalized, offsets := dlpNormalized(value)
	findings := make([]DLPFinding, 0)
	record := func(start, end int, kind DLPKind, level Sensitivity) {
		if offsets != nil {
			start, end = offsets[start], offsets[end]
		}
		findings = append(findings, DLPFinding{Start: start, End: end, Kind: kind, Sensitivity: level})
	}
	for _, match := range dlpEmails.FindAllStringIndex(normalized, -1) {
		record(match[0], match[1], DLPEmail, SensitivityConfidential)
	}
	for _, match := range dlpIDs.FindAllStringIndex(normalized, -1) {
		if digitBoundary(normalized, match[0], match[1]) && validNationalID(normalized[match[0]:match[1]]) {
			record(match[0], match[1], DLPNationalID, SensitivityConfidential)
		}
	}
	for _, match := range dlpPhones.FindAllStringIndex(normalized, -1) {
		if digitBoundary(normalized, match[0], match[1]) {
			record(match[0], match[1], DLPMobile, SensitivityConfidential)
		}
	}
	for _, match := range paymentCards(normalized) {
		record(match[0], match[1], DLPPayment, SensitivityConfidential)
	}
	for _, pattern := range s.custom {
		for _, match := range pattern.regexp.FindAllStringSubmatchIndex(normalized, -1) {
			if pattern.kind == DLPFloorPrice {
				match = match[2:]
			}
			record(match[0], match[1], pattern.kind, pattern.sensitivity)
		}
	}
	return mergeFindings(findings)
}

func mergeFindings(findings []DLPFinding) []DLPFinding {
	slices.SortFunc(findings, func(a, b DLPFinding) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		if a.End != b.End {
			return b.End - a.End
		}
		return sensitivityRank(b.Sensitivity) - sensitivityRank(a.Sensitivity)
	})
	out := findings[:0]
	for _, item := range findings {
		if len(out) == 0 || out[len(out)-1].End <= item.Start {
			out = append(out, item)
			continue
		}
		current := &out[len(out)-1]
		current.End = max(current.End, item.End)
		if sensitivityRank(item.Sensitivity) > sensitivityRank(current.Sensitivity) {
			current.Kind, current.Sensitivity = item.Kind, item.Sensitivity
		}
	}
	return out
}

func dlpNormalized(value string) (string, []int) {
	var normalized strings.Builder
	var offsets []int
	for index, r := range value {
		var mapped rune
		if r >= '！' && r <= '～' {
			mapped = r - '！' + '!'
		} else if r == '　' {
			mapped = ' '
		}
		if mapped != 0 {
			if offsets == nil {
				normalized.Grow(len(value))
				normalized.WriteString(value[:index])
				offsets = make([]int, index, len(value)+1)
				for i := range offsets {
					offsets[i] = i
				}
			}
			normalized.WriteRune(mapped)
			offsets = append(offsets, index)
		} else if offsets != nil {
			normalized.WriteRune(r)
			for i := range utf8.RuneLen(r) {
				offsets = append(offsets, index+i)
			}
		}
	}
	if offsets == nil {
		return value, nil
	}
	return normalized.String(), append(offsets, len(value))
}

func asciiDigit(b byte) bool { return b >= '0' && b <= '9' }

func digitBoundary(value string, start, end int) bool {
	return (start == 0 || !asciiDigit(value[start-1])) && (end == len(value) || !asciiDigit(value[end]))
}

func validNationalID(value string) bool {
	const final = "10X98765432"
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	if len(value) != 18 {
		return false
	}
	year, yerr := strconv.Atoi(value[6:10])
	month, merr := strconv.Atoi(value[10:12])
	day, derr := strconv.Atoi(value[12:14])
	if yerr != nil || merr != nil || derr != nil {
		return false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return false
	}
	total := 0
	for i, weight := range weights {
		if !asciiDigit(value[i]) {
			return false
		}
		total += int(value[i]-'0') * weight
	}
	last := value[17]
	if last == 'x' {
		last = 'X'
	}
	return last == final[total%11]
}

func paymentCards(value string) [][2]int {
	numbers := dlpDigitRuns.FindAllStringIndex(value, -1)
	var out [][2]int
	layouts := [...][5]int{
		{4, 4, 4, 4, 3}, {4, 4, 4, 4, 2}, {4, 4, 4, 4, 1}, {4, 4, 4, 4}, {4, 4, 4, 3},
		{4, 4, 4, 2}, {4, 4, 4, 1}, {4, 6, 5},
	}
	for i, group := range numbers {
		start, end := group[0], group[1]
		if end-start >= 13 && end-start <= 19 && luhn(value[start:end]) {
			out = append(out, [2]int{start, end})
		}
		if end-start != 4 {
			continue
		}
		for _, layout := range layouts {
			end, ok := groupedCardEnd(value, numbers[i:], layout)
			if ok && luhn(value[start:end]) {
				out = append(out, [2]int{start, end})
				break
			}
		}
	}
	return out
}

func groupedCardEnd(value string, numbers [][]int, layout [5]int) (int, bool) {
	end := numbers[0][0]
	for i, size := range layout {
		if size == 0 {
			break
		}
		if i >= len(numbers) || numbers[i][1]-numbers[i][0] != size ||
			(i > 0 && strings.Trim(value[end:numbers[i][0]], "- \t") != "") {
			return 0, false
		}
		end = numbers[i][1]
	}
	return end, true
}

func luhn(value string) bool {
	total, digits := 0, 0
	repeated := true
	first := byte(0)
	for i := len(value) - 1; i >= 0; i-- {
		b := value[i]
		if !asciiDigit(b) {
			continue
		}
		if digits == 0 {
			first = b
		} else if b != first {
			repeated = false
		}
		n := int(b - '0')
		if digits%2 == 1 {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		total += n
		digits++
	}
	return digits >= 13 && digits <= 19 && !repeated && total%10 == 0
}
