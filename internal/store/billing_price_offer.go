package store

import (
	"fmt"
	"math/big"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billingvalue"
)

// PointPriceRates is the administrator's reference price before a model discount.
type PointPriceRates struct {
	Input      string `json:"input_per_million"`
	Output     string `json:"output_per_million"`
	CacheRead  string `json:"cache_read_per_million"`
	CacheWrite string `json:"cache_write_per_million"`
}

// NormalizePointPrice stores the same effective rates used by the catalog and
// request price snapshots. A 10 percent discount means charging 90 percent.
func NormalizePointPrice(p PointPrice) (PointPrice, error) {
	if p.DiscountPercent < 0 || p.DiscountPercent > 99 {
		return p, fmt.Errorf("discount_percent must be an integer between 0 and 99")
	}
	base := PointPriceRates{p.Input, p.Output, p.CacheRead, p.CacheWrite}
	if p.DiscountPercent > 0 {
		if p.StandardPrice == nil {
			return p, fmt.Errorf("discounted models require a standard_price")
		}
		base = *p.StandardPrice
	}
	inputs := []*string{&base.Input, &base.Output, &base.CacheRead, &base.CacheWrite}
	outputs := []*string{&p.Input, &p.Output, &p.CacheRead, &p.CacheWrite}
	for i, value := range inputs {
		normalized, err := billingvalue.Normalize(*value, false)
		if err != nil {
			return p, err
		}
		*value = normalized
		rate, _ := new(big.Rat).SetString(normalized)
		rate.Mul(rate, new(big.Rat).SetFrac64(int64(100-p.DiscountPercent), 100))
		*outputs[i] = rate.FloatString(6)
		if rate.Sign() > 0 && *outputs[i] == "0.000000" {
			return p, fmt.Errorf("discounted rate is below the supported precision")
		}
	}
	p.StandardPrice = nil
	if p.DiscountPercent > 0 {
		p.StandardPrice = &base
	}
	return p, nil
}
