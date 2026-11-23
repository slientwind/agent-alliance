package runtime

import (
	"math/big"
	"regexp"
	"strings"
)

var measuredValue = regexp.MustCompile(`(?i)(零下|负|−|[+\-])?\s*([0-9]+(?:,[0-9]{3})*(?:\.[0-9]+)?)\s*(个百分点|摄氏度|℃|°\s*c|华氏度|℉|°\s*f|%|万美元|万元|美元|欧元|港元|日元|人民币元|元)`)

// Only explicit temperatures, monetary amounts and percentages are checked.
// Keep currencies and percentage points distinct. Compare decimals exactly.
func lookupQuantities(text string) map[string]bool {
	text = strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return '0' + r - '０'
		}
		switch r {
		case '．':
			return '.'
		case '，':
			return ','
		case '％':
			return '%'
		case '－':
			return '-'
		}
		return r
	}, text)
	values := map[string]bool{}
	for _, m := range measuredValue.FindAllStringSubmatch(text, -1) {
		n, ok := new(big.Rat).SetString(strings.ReplaceAll(m[2], ",", ""))
		if !ok {
			continue
		}
		if m[1] != "" && m[1] != "+" {
			n.Neg(n)
		}
		unit := strings.ToLower(strings.Join(strings.Fields(m[3]), ""))
		switch unit {
		case "摄氏度", "℃":
			unit = "°c"
		case "华氏度", "℉":
			unit = "°f"
		case "人民币元":
			unit = "元"
		case "万元", "万美元":
			n.Mul(n, big.NewRat(10000, 1))
			unit = strings.TrimPrefix(unit, "万")
		}
		values[n.RatString()+unit] = true
	}
	return values
}

func quantitiesSupported(answer string, cited []string) bool {
	required := lookupQuantities(answer)
	if len(required) == 0 {
		return true
	}
	found := lookupQuantities(strings.Join(cited, "\n"))
	for quantity := range required {
		if !found[quantity] {
			return false
		}
	}
	return true
}
