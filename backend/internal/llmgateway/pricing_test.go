package llmgateway

import (
	"math"
	"testing"
)

func TestCostMicroCNY(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price Price
		usage Usage
		want  int64
		bad   bool
	}{
		{"different token and image prices", Price{1_000_000, 3_000_000, 250_000, 120}, Usage{10, 2, 4, 3}, 373, false},
		{"round all tokens once", Price{250_000, 250_000, 0, 0}, Usage{1, 1, 0, 0}, 1, false},
		{"below half", Price{499_999, 0, 0, 0}, Usage{1, 0, 0, 0}, 0, false},
		{"intermediate product exceeds 64 bits", Price{1_000_000, 0, 0, 0}, Usage{math.MaxInt64, 0, 0, 0}, math.MaxInt64, false},
		{"final overflow", Price{1_000_000, 0, 0, 1}, Usage{math.MaxInt64, 0, 0, 1}, 0, true},
		{"cache is a subset of input", Price{}, Usage{1, 0, 2, 0}, 0, true},
		{"negative prices are invalid", Price{-1, 0, 0, 0}, Usage{}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.price.CostMicroCNY(tc.usage)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %d, err=%v; want %d, error=%t", got, err, tc.want, tc.bad)
			}
		})
	}
}
