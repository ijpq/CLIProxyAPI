package billing

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ModelPrice describes per-million-token prices for one model. Cache prices
// fall back to the input price when zero so providers without cache pricing
// still produce a reasonable cost estimate.
type ModelPrice struct {
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
}

// PricingTable maps "provider/model" (preferred) or "model" (fallback) to a
// price entry. Keys are matched case-insensitively after trimming whitespace.
type PricingTable struct {
	mu     sync.RWMutex
	prices map[string]ModelPrice
	markup float64
}

// NewPricingTable constructs an empty table with the supplied markup
// multiplier (1.0 = pass through). A non-positive markup defaults to 1.0.
func NewPricingTable(markup float64) *PricingTable {
	if markup <= 0 {
		markup = 1.0
	}
	return &PricingTable{prices: make(map[string]ModelPrice), markup: markup}
}

// LoadPricingFromFile reads a JSON pricing document from path. The file must
// decode into map[string]ModelPrice. Missing files return an empty table so
// operators can start without pricing configured (cost will be zero).
func LoadPricingFromFile(path string, markup float64) (*PricingTable, error) {
	table := NewPricingTable(markup)
	if strings.TrimSpace(path) == "" {
		return table, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return table, nil
		}
		return nil, fmt.Errorf("billing: read pricing file: %w", err)
	}
	raw := make(map[string]ModelPrice)
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("billing: parse pricing file: %w", err)
	}
	for k, v := range raw {
		table.prices[normalizeKey(k)] = v
	}
	return table, nil
}

// Lookup returns the price entry for the given provider/model, preferring the
// fully-qualified key. The bool reports whether an entry was found.
func (t *PricingTable) Lookup(provider, model string) (ModelPrice, bool) {
	if t == nil {
		return ModelPrice{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if provider != "" {
		if p, ok := t.prices[normalizeKey(provider+"/"+model)]; ok {
			return p, true
		}
	}
	if p, ok := t.prices[normalizeKey(model)]; ok {
		return p, true
	}
	return ModelPrice{}, false
}

// Cost computes the total cost in the proxy's billing currency given a token
// breakdown. Returns 0 when no price entry matches.
func (t *PricingTable) Cost(provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	price, ok := t.Lookup(provider, model)
	if !ok {
		return 0
	}
	cost := perMillion(price.InputPerMillion, inputTokens) +
		perMillion(price.OutputPerMillion, outputTokens) +
		perMillion(price.CacheReadPerMillion, cacheReadTokens) +
		perMillion(price.CacheWritePerMillion, cacheWriteTokens)
	return cost * t.markup
}

func perMillion(pricePerMillion float64, tokens int64) float64 {
	if pricePerMillion <= 0 || tokens <= 0 {
		return 0
	}
	return pricePerMillion * float64(tokens) / 1_000_000.0
}

func normalizeKey(k string) string {
	return strings.ToLower(strings.TrimSpace(k))
}
