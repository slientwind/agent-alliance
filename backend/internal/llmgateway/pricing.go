package llmgateway

import (
	"errors"
	"math/big"
)

const tokensPerRate = 1_000_000

type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	Images           int64
}

// CostMicroCNY charges cached prompt tokens at their own rate. All token
// categories share one rounding step to the nearest micro-CNY, with ties up.
func (p Price) CostMicroCNY(u Usage) (int64, error) {
	if p.InputPerMillion < 0 || p.OutputPerMillion < 0 || p.CachedPerMillion < 0 || p.PerImage < 0 ||
		u.PromptTokens < 0 || u.CompletionTokens < 0 || u.CachedTokens < 0 || u.CachedTokens > u.PromptTokens || u.Images < 0 {
		return 0, errors.New("invalid price or model usage")
	}
	var total, term big.Int
	for _, pair := range [][2]int64{
		{u.PromptTokens - u.CachedTokens, p.InputPerMillion},
		{u.CompletionTokens, p.OutputPerMillion},
		{u.CachedTokens, p.CachedPerMillion},
	} {
		term.Mul(big.NewInt(pair[0]), big.NewInt(pair[1]))
		total.Add(&total, &term)
	}
	total.Add(&total, big.NewInt(tokensPerRate/2))
	total.Quo(&total, big.NewInt(tokensPerRate))
	term.Mul(big.NewInt(u.Images), big.NewInt(p.PerImage))
	total.Add(&total, &term)
	if !total.IsInt64() {
		return 0, errors.New("model cost overflows micro-CNY amount")
	}
	return total.Int64(), nil
}
