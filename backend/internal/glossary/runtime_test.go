package glossary

import (
	"slices"
	"testing"
)

func TestQueryExpansionKeepsScopedTermsAndWordBoundaries(t *testing.T) {
	items := []Term{
		{Term: "商品成交额", Aliases: []string{"GMV", "成交总额"}},
		{Term: "第三方交易额", Aliases: []string{"GMV", "外渠道金额"}},
		{Term: "履约物流", Aliases: []string{"配送状态"}},
	}
	expanded := expandMatch("今天的 ＧＭＶ 和配送状态", items, 8)
	if !slices.Equal(expanded, []string{"商品成交额", "成交总额", "履约物流"}) {
		t.Fatalf("unexpected term expansion: %v", expanded)
	}
	if expanded := expandMatch("product-AGMV-42", items, 8); len(expanded) != 0 {
		t.Fatalf("identifier matched inside an unrelated token: %v", expanded)
	}
}
