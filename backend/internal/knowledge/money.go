package knowledge

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var sourceMoney = regexp.MustCompile(`(负|−|[+-])?([0-9]+(?:,[0-9]{3})*(?:\.[0-9]+)?)\s*(人民币元|美元|欧元|港元|日元|元)`)

func HasMoneyLiteral(text string) bool { return sourceMoney.MatchString(text) }

func FirstMoneyPosition(text string) int {
	pos := sourceMoney.FindStringIndex(text)
	if pos == nil {
		return -1
	}
	return pos[0]
}

type CitedMoney struct {
	Start             int
	Literal, Currency string
	Cents             *big.Int
}

// Reject a fragment of an unsupported number or accounting notation. Inspect
// the source before the excerpt as a 320-character search window can cut a token.
func startsMoney(before string) bool {
	prefix := strings.TrimRightFunc(before, unicode.IsSpace)
	if prefix == "" {
		return true
	}
	r, size := utf8.DecodeLastRuneInString(prefix)
	if strings.ContainsRune("+-－−负(（$￥¥", r) || unicode.IsDigit(r) {
		return false
	}
	if r == ',' || r == '，' || r == '.' || r == '．' {
		prev, _ := utf8.DecodeLastRuneInString(prefix[:len(prefix)-size])
		return !unicode.IsDigit(prev) && r != '.' && r != '．'
	}
	return before != prefix || !(r < 128 && unicode.IsLetter(r)) || currencyPrefix.MatchString(prefix)
}

var currencyPrefix = regexp.MustCompile(`(人民币|人民幣|港币|港幣|港元|美元|美金|欧元|日元|日币|\b[A-Z]{3})\s*$`)
var fractionalSuffix = regexp.MustCompile(`^\s*[0-9零〇一二两三四五六七八九十]+\s*[角分毛]`)

// Qualifiers on either side matter: “港币 100 元” and “100 美元” cannot
// enter the same sum. Reject compound amounts we cannot express faithfully.
func operandCurrency(before, after, suffix string) (string, bool) {
	if fractionalSuffix.MatchString(after) {
		return "", false
	}
	match := currencyPrefix.FindStringSubmatch(before)
	if match == nil {
		prefix := strings.TrimSpace(before)
		if strings.HasSuffix(prefix, "币") || strings.HasSuffix(prefix, "幣") || strings.HasSuffix(prefix, "英镑") || strings.HasSuffix(prefix, "法郎") {
			return "", false
		}
		return suffix, true
	}
	var unit string
	switch strings.ToUpper(match[1]) {
	case "人民币", "人民幣", "CNY", "RMB":
		unit = "人民币元"
	case "港币", "港幣", "港元", "HKD":
		unit = "港元"
	case "美元", "美金", "USD":
		unit = "美元"
	case "欧元", "EUR":
		unit = "欧元"
	case "日元", "日币", "JPY":
		unit = "日元"
	default:
		return "", false
	}
	if suffix != "元" && suffix != unit {
		return "", false
	}
	return unit, true
}

func MoneyInExcerpt(body string, from, to int) []CitedMoney {
	source := []rune(body)
	if from < 0 || from >= to || to > len(source) {
		return nil
	}
	excerpt := string(source[from:to])
	prefix := string(source[max(0, from-12):from])
	beyond := string(source[to:min(len(source), to+16)])
	var amounts []CitedMoney
	for _, match := range sourceMoney.FindAllStringSubmatchIndex(excerpt, -1) {
		start, end := match[0], match[1]
		if !startsMoney(prefix + excerpt[:start]) {
			continue
		}
		number := strings.ReplaceAll(excerpt[match[4]:match[5]], ",", "")
		whole, fraction, _ := strings.Cut(number, ".")
		if len(whole) > 14 || len(fraction) > 2 {
			continue
		}
		cents, ok := new(big.Int).SetString(whole+(fraction + "00")[:2], 10)
		if !ok {
			continue
		}
		if match[2] >= 0 && excerpt[match[2]:match[3]] != "+" {
			cents.Neg(cents)
		}
		unit, ok := operandCurrency(prefix+excerpt[:start], excerpt[end:]+beyond, excerpt[match[6]:match[7]])
		if !ok {
			continue
		}
		amounts = append(amounts, CitedMoney{from + utf8.RuneCountInString(excerpt[:start]), excerpt[start:end], unit, cents})
	}
	return amounts
}
