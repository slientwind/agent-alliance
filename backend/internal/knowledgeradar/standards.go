package knowledgeradar

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const standardAmount = `(-?[0-9][0-9,]{0,17}(?:\.[0-9]{1,10})?)\s*(万元|万人民币|人民币|元|美元|欧元|CNY|RMB|USD|EUR|工作日|天|日|小时|分钟|%|千克|公斤|吨|件|次)(\s*/\s*(?:晚|天|日|月|年|人|笔|次|台|件|吨|千克|公斤))?`

var (
	standardClause = regexp.MustCompile(`(?i)([\p{L}\p{N}（）()·_\-]{2,42}?)(?:的)?\s*(不得超过|不超过|最高限额|最高|上限|限额|至多|不高于|至少|不得低于|不低于|下限|最低|标准|额度)\s*(?:为|是|按|[:：=]|≤|≥)?\s*` + standardAmount)
	standardCell   = regexp.MustCompile(`(?i)^\s*(?:[¥￥])?\s*` + standardAmount + `\s*$`)
	sectionPrefix  = regexp.MustCompile(`^第[一二三四五六七八九十百0-9]+[条章节]\s*`)
)

type standardClaim struct {
	metric, label, comparator, value, unit string
}

func standardComparator(value string) string {
	switch value {
	case "至少", "不得低于", "不低于", "下限", "最低":
		return "lower"
	case "标准", "额度":
		return "exact"
	}
	return "upper"
}

func parseStandard(label, comparator, rawValue, unit, rate string) (standardClaim, bool) {
	label = strings.TrimSpace(sectionPrefix.ReplaceAllString(label, ""))
	label = strings.Trim(label, " .·-—；：")
	if utf8.RuneCountInString(label) < 2 || utf8.RuneCountInString(label) > 85 || strings.ContainsFunc(label, unicode.IsControl) {
		return standardClaim{}, false
	}
	number, ok := new(big.Rat).SetString(strings.ReplaceAll(rawValue, ",", ""))
	if !ok {
		return standardClaim{}, false
	}
	unit, rate = strings.ToUpper(unit), strings.ReplaceAll(strings.TrimSpace(rate), " ", "")
	switch unit {
	case "万元", "万人民币":
		number.Mul(number, big.NewRat(10000, 1))
		unit = "元"
	case "CNY", "RMB", "人民币":
		unit = "元"
	case "USD":
		unit = "美元"
	case "EUR":
		unit = "欧元"
	case "日":
		unit = "天"
	case "千克":
		unit = "公斤"
	}
	for _, suffix := range []string{"每晚", "每天", "每日", "每月", "每年", "每人", "每笔", "每次"} {
		if base, ok := strings.CutSuffix(label, suffix); ok {
			inferred := "/" + strings.TrimPrefix(suffix, "每")
			if rate == "" || rate == inferred {
				label, rate = base, inferred
			}
			break
		}
	}
	if rate == "/日" {
		rate = "/天"
	}
	if rate == "/千克" {
		rate = "/公斤"
	}
	metric := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("·：:", r) {
			return -1
		}
		return unicode.ToLower(r)
	}, label)
	if metric == "" {
		return standardClaim{}, false
	}
	return standardClaim{metric: metric, label: label, comparator: comparator, value: number.FloatString(10), unit: unit + rate}, true
}

func standardClaims(body string, breadcrumb []string) []standardClaim {
	body = norm.NFKC.String(body)
	var result []standardClaim
	for _, clause := range standardClause.FindAllStringSubmatch(body, 80) {
		if claim, ok := parseStandard(clause[1], standardComparator(clause[2]), clause[3], clause[4], clause[5]); ok {
			result = append(result, claim)
		}
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], " | ") {
		return result
	}
	columns := strings.Split(lines[0], " | ")
	section := ""
	if len(breadcrumb) > 0 {
		section = strings.TrimSpace(breadcrumb[len(breadcrumb)-1]) + " · "
	}
	for _, line := range lines[1:] {
		cells := strings.Split(line, " | ")
		if len(cells) != len(columns) || strings.TrimSpace(cells[0]) == "" {
			continue
		}
		for column, header := range columns[1:] {
			comparison, metric := "", strings.TrimSpace(header)
			for _, label := range []string{"上限", "最高", "限额", "下限", "最低", "标准"} {
				if stem, ok := strings.CutSuffix(metric, label); ok {
					metric, comparison = strings.TrimSpace(stem), standardComparator(label)
					break
				}
			}
			if comparison == "" || len(result) >= 80 {
				continue
			}
			amount := standardCell.FindStringSubmatch(cells[column+1])
			if amount == nil {
				continue
			}
			label := strings.TrimSpace(section + cells[0] + " " + metric)
			if claim, ok := parseStandard(label, comparison, amount[1], amount[2], amount[3]); ok {
				result = append(result, claim)
			}
		}
	}
	return result
}
