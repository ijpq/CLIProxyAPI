package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billingvalue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type PointPriceReader interface {
	ListPointPrices(context.Context) ([]store.PointPrice, error)
}

type pointPriceKey struct{}

func WithPointPrice(ctx context.Context, price store.PointPrice) context.Context {
	return context.WithValue(ctx, pointPriceKey{}, price)
}

// PointPriceGuard binds the advertised price version before any paid execution.
// Requests without a resolvable model cannot enter paid execution implicitly.
func PointPriceGuard(reader PointPriceReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		if UserIDFromContext(ctx) == "" || UnbilledFromContext(ctx) {
			return
		}
		if c.Request.Method == "GET" && strings.Contains(c.Request.URL.Path, "/models") {
			return
		}
		requested, parseErr := paidRequestedModel(c)
		if parseErr != nil {
			c.AbortWithStatusJSON(400, gin.H{"error": parseErr.Error()})
			return
		}
		model := normalizeAccessModel(requested)
		if model == "" {
			c.AbortWithStatusJSON(400, gin.H{"error": "paid requests require an explicit model in JSON or the model URL"})
			return
		}
		prices, err := reader.ListPointPrices(ctx)
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "points pricing unavailable"})
			return
		}
		for _, price := range prices {
			if price.Enabled && strings.EqualFold(price.Model, model) {
				c.Request = c.Request.WithContext(WithPointPrice(ctx, price))
				return
			}
		}
		c.AbortWithStatusJSON(403, gin.H{"error": "model is not available for points billing"})
	}
}

// Paid admission rejects ambiguous JSON before selecting a price snapshot.
func paidRequestedModel(c *gin.Context) (string, error) {
	if c.Request.Body == nil {
		return requestedModel(c), nil
	}
	original := c.Request.Body
	data, err := io.ReadAll(io.LimitReader(original, (8<<20)+1))
	c.Request.Body = &replayRequestBody{Reader: io.MultiReader(bytes.NewReader(data), original), Closer: original}
	if err != nil || len(data) > 8<<20 {
		return "", fmt.Errorf("paid JSON requests must fit within 8 MiB")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return requestedModel(c), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", fmt.Errorf("paid requests require a JSON object")
	}
	seen := map[string]bool{}
	model := ""
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("invalid paid request JSON")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return "", fmt.Errorf("duplicate JSON fields are not accepted for paid requests")
		}
		seen[name] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return "", fmt.Errorf("invalid paid request JSON")
		}
		if name == "model" {
			if err = json.Unmarshal(value, &model); err != nil {
				return "", fmt.Errorf("model must be a string")
			}
		}
	}
	if _, err = decoder.Token(); err != nil {
		return "", fmt.Errorf("invalid paid request JSON")
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return "", fmt.Errorf("multiple JSON values are not accepted for paid requests")
	}
	if strings.TrimSpace(model) == "" {
		return requestedModel(c), nil
	}
	return model, nil
}

func pointsCost(ctx context.Context, input, output, cacheRead, cacheWrite int64) (string, int64, error) {
	price, ok := ctx.Value(pointPriceKey{}).(store.PointPrice)
	if !ok {
		return "0.000000", 0, nil
	}
	cost, err := billingvalue.TokenCost([]string{price.Input, price.Output, price.CacheRead, price.CacheWrite}, []int64{input, output, cacheRead, cacheWrite})
	return cost, price.Version, err
}
