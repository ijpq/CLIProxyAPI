// Package billingvalue implements fixed-precision, nonnegative billing values.
package billingvalue

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,13})(\.[0-9]{1,6})?$`)

// Normalize validates a NUMERIC(20,6) value without floating point conversion.
func Normalize(raw string, positive bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if !decimalPattern.MatchString(raw) {
		return "", fmt.Errorf("use a nonnegative decimal with at most six fractional digits")
	}
	r, _ := new(big.Rat).SetString(raw)
	if positive && r.Sign() == 0 {
		return "", fmt.Errorf("value must be positive")
	}
	return r.FloatString(6), nil
}

func NormalizeSigned(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = strings.TrimPrefix(raw, "-")
	}
	value, err := Normalize(raw, false)
	if err != nil {
		return "", err
	}
	if negative && value != "0.000000" {
		value = "-" + value
	}
	return value, nil
}

// TokenCost rounds a sum of per-million-token charges to six decimal places.
func TokenCost(prices []string, tokens []int64) (string, error) {
	total := new(big.Rat)
	for i, p := range prices {
		value, err := Normalize(p, false)
		if err != nil {
			return "", err
		}
		if i >= len(tokens) || tokens[i] <= 0 {
			continue
		}
		rate, _ := new(big.Rat).SetString(value)
		total.Add(total, rate.Mul(rate, new(big.Rat).SetFrac64(tokens[i], 1_000_000)))
	}
	result := total.FloatString(6)
	if _, err := Normalize(result, false); err != nil {
		return "", fmt.Errorf("points charge exceeds the supported range")
	}
	return result, nil
}
