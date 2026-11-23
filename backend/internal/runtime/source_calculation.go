package runtime

import (
	"encoding/json"
	"fmt"
	"math/big"

	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"alliance/internal/knowledge"
)

const moneySearchInstructions = "先定位计算涉及的业务对象和金额。针对各对象选二至四个简短的原文检索词，只返回带 terms 数组的 JSON。算式、差额、合计和原文依据是输出要求，优先检索它们所涉及的对象。"

const calculateInstructions = "如果任务要求求和或差额，请使用本地金额计算工具。每个金额编号对应下方给出的原文位置，只选所需的同币种金额。只返回 JSON，如 {\"calculation\":{\"operation\":\"difference\",\"operands\":[2,1]}}，difference 表示前一项减后一项；sum 表示相加。operands 填金额编号，不填金额本身。先看清每个编号的引文，差额以用户指定的被减项为首项。不要自行心算。无法从引文中选全所需金额、币种不同或业务口径不清时，返回 {\"question\":\"需要用户补充的具体信息\"}。"

type moneyOperand struct {
	hit               int
	literal, currency string
	cents             *big.Int
}

func moneyOperands(hits []lookupHit, files []knowledge.InputSource) []moneyOperand {
	var amounts []moneyOperand
	seen := map[[2]int]bool{}
	for i, h := range hits {
		for _, m := range knowledge.MoneyInExcerpt(files[h.source].Text, h.start, h.end) {
			key := [2]int{h.source, m.Start}
			if !seen[key] {
				seen[key] = true
				amounts = append(amounts, moneyOperand{i, m.Literal, m.Currency, m.Cents})
			}
		}
	}
	return amounts
}

// A model may ask for “实付比” while a receipt says “实付”. In calculation
// mode a matching phrase fragment plus an explicit monetary amount gives a
// second retrieval signal. It only fills in files missing monetary evidence.
func addMoneyEvidence(hits []lookupHit, terms []string, inputs []sourceSentences, files []knowledge.InputSource, goal string) []lookupHit {
	anchors := map[string]bool{}
	for _, term := range terms {
		runes := []rune(term)
		for i := 1; i < len(runes); i++ {
			if unicode.Is(unicode.Han, runes[i-1]) && unicode.Is(unicode.Han, runes[i]) {
				pair := string(runes[i-1 : i+1])
				if strings.Contains(goal, pair) {
					anchors[pair] = true
				}
			}
		}
	}
	found := map[int]bool{}
	for _, h := range hits {
		if knowledge.HasMoneyLiteral(h.text) {
			found[h.source] = true
		}
	}
	for i, src := range inputs {
		if found[i] {
			continue
		}
		for _, clause := range src.clauses {
			amount := knowledge.FirstMoneyPosition(clause.Text)
			if amount < 0 {
				continue
			}
			anchor := clause.Start - 1 + utf8.RuneCountInString(clause.Text[:amount])
			start := max(clause.Start-1, anchor-120)
			end := min(start+320, clause.End)
			quote := string(src.runes[start:end])
			score := 0
			for pair := range anchors {
				if strings.Contains(quote, pair) {
					score++
				}
			}
			if score > 0 {
				hits = append(hits, lookupHit{i, files[i].Version, start, end, score, quote})
			}
		}
	}
	return hits
}

func withReceiptHits(original []lookupHit, files []knowledge.InputSource) []lookupHit {
	receipts := receiptHits(files)
	if len(receipts) == 0 {
		return original
	}
	// Keep monetary clauses from the initial search alongside the explicitly
	// supplied passages. Remaining context fills the same six-citation window.
	var evidence, other []lookupHit
	for _, h := range original {
		if files[h.source].ReceiptStep != 0 {
			continue
		}
		if knowledge.HasMoneyLiteral(h.text) {
			evidence = append(evidence, h)
		} else {
			other = append(other, h)
		}
	}
	// Prefer the newest receipt and retain a compatible earlier operand even
	// when earlier (incompatible) currencies filled the six excerpt slots.
	slices.Reverse(receipts)
	all := append(receipts, evidence...)
	all = append(all, other...)
	if len(all) <= 6 {
		return all
	}
	newest := moneyOperands(all[:1], files)
	selected := -1
	for _, operand := range moneyOperands(all[1:], files) {
		if slices.ContainsFunc(newest, func(m moneyOperand) bool { return m.currency == operand.currency }) {
			selected = operand.hit + 1
			break
		}
	}
	if selected >= 6 {
		all[5] = all[selected]
	}
	return all[:6]
}

func calculationGap(operands []moneyOperand) string {
	if len(operands) < 2 {
		return "所选原文中可核对的金额不足两项，请补充缺少的金额及币种。"
	}
	seen := map[string]bool{}
	for _, a := range operands {
		if seen[a.currency] {
			return ""
		}
		seen[a.currency] = true
	}
	return "原文里的金额币种不同，请提供同币种的金额或已核准的换算结果。"
}

func calculationContext(operands []moneyOperand) string {
	var b strings.Builder
	b.WriteString("\n可用金额编号（取自以上引文）：\n")
	for i, a := range operands {
		fmt.Fprintf(&b, "金额 %d = %s，出自引文 [%d]\n", i+1, a.literal, a.hit+1)
	}
	return b.String()
}

type moneyAction struct {
	Operation string
	Operands  []int
}

func calculationAnswer(raw string, hits []lookupHit, files []knowledge.InputSource, operands []moneyOperand) (string, string) {
	var request map[string]json.RawMessage
	if !decodeLookup(raw, &request) || request["calculation"] == nil {
		return groundedAnswer(raw, hits, files)
	}
	invalid := func() (string, string) {
		return "无法按这些引文核实计算口径。请核对金额、币种与计算顺序。\n" + lookupContext(hits, files), "lookup_calculation_rejected"
	}
	var action struct{ Calculation moneyAction }
	if len(request) != 1 || !decodeLookup(raw, &action) {
		return invalid()
	}
	op, ids := action.Calculation.Operation, action.Calculation.Operands
	if (op != "sum" && op != "difference") || len(ids) < 2 || len(ids) > 6 || (op == "difference" && len(ids) != 2) {
		return invalid()
	}
	total := new(big.Int)
	var unit string
	var terms, refs []string
	seen := map[int]bool{}
	var cited []int
	for i, id := range ids {
		if id < 1 || id > len(operands) || seen[id] {
			return invalid()
		}
		a := operands[id-1]
		if i > 0 && unit != a.currency {
			return invalid()
		}
		unit = a.currency
		seen[id] = true
		if op == "difference" && i == 1 {
			total.Sub(total, a.cents)
		} else {
			total.Add(total, a.cents)
		}
		terms = append(terms, fmt.Sprintf("%s [%d]", a.literal, a.hit+1))
		if !slices.Contains(cited, a.hit) {
			h := hits[a.hit]
			refs = append(refs, fmt.Sprintf("[%d] %s v%d %s", a.hit+1, strings.Join(strings.Fields(files[h.source].Name), " "), h.version, h.line()))
			cited = append(cited, a.hit)
		}
	}
	join, label := " + ", "合计"
	if op == "difference" {
		join, label = " − ", "差额"
	}
	decimal := new(big.Rat).SetFrac(total, big.NewInt(100)).FloatString(2)
	return fmt.Sprintf("%s为 %s %s。\n%s = %s %s\n\n原文依据\n%s", label, decimal, unit, strings.Join(terms, join), decimal, unit, strings.Join(refs, "\n")), "lookup_calculated"
}
