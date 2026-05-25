package billing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// MeterSink is the subset of the Postgres store used by the meter plugin.
// Defined as an interface so tests can substitute a fake.
type MeterSink interface {
	RecordUsageAndDebit(ctx context.Context, u store.MeteredUsage) error
}

// MeterPlugin is a usage.Plugin that translates each Record into a billing
// event: it looks up pricing, computes cost, and writes the usage row plus a
// wallet debit transactionally.
type MeterPlugin struct {
	sink             MeterSink
	pricing          *PricingTable
	invalidator      func(userID string)
	notifier         Notifier
	balanceReader    BalanceReader
	lowBalanceThresh float64
}

// NewMeterPlugin builds the plugin. Both arguments are required; passing nil
// for either disables billing for the request.
func NewMeterPlugin(sink MeterSink, pricing *PricingTable) *MeterPlugin {
	return &MeterPlugin{sink: sink, pricing: pricing}
}

// SetInvalidator registers a callback invoked after each successful debit so
// downstream caches (e.g. the BalanceGuard) can drop stale entries.
func (p *MeterPlugin) SetInvalidator(fn func(userID string)) {
	if p == nil {
		return
	}
	p.invalidator = fn
}

// SetLowBalanceNotifier enables notifications when a user's balance drops
// below the given threshold after a debit.
func (p *MeterPlugin) SetLowBalanceNotifier(n Notifier, reader BalanceReader, threshold float64) {
	if p == nil {
		return
	}
	p.notifier = n
	p.balanceReader = reader
	p.lowBalanceThresh = threshold
}

// HandleUsage implements usage.Plugin.
func (p *MeterPlugin) HandleUsage(ctx context.Context, record usage.Record) {
	if p == nil || p.sink == nil {
		return
	}
	userID := UserIDFromContext(ctx)
	if userID == "" {
		// Request was authenticated through a non-billing provider (legacy
		// config-api-key, internal management); nothing to meter.
		return
	}

	cost := 0.0
	if p.pricing != nil {
		cost = p.pricing.Cost(
			record.Provider, record.Model,
			record.Detail.InputTokens, record.Detail.OutputTokens,
			record.Detail.CacheReadTokens, record.Detail.CacheCreationTokens,
		)
	}

	status := "success"
	errMsg := ""
	if record.Failed {
		status = "error"
		errMsg = record.Fail.Body
		if len(errMsg) > 512 {
			errMsg = errMsg[:512]
		}
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.sink.RecordUsageAndDebit(bgCtx, store.MeteredUsage{
		UserID:           userID,
		APIKeyID:         APIKeyIDFromContext(ctx),
		RequestID:        requestIDFromContext(ctx),
		Provider:         record.Provider,
		Model:            record.Model,
		InputTokens:      record.Detail.InputTokens,
		OutputTokens:     record.Detail.OutputTokens,
		CacheReadTokens:  record.Detail.CacheReadTokens,
		CacheWriteTokens: record.Detail.CacheCreationTokens,
		Cost:             fmt.Sprintf("%.6f", cost),
		Status:           status,
		ErrorMessage:     errMsg,
	}); err != nil {
		log.WithError(err).Errorf("billing: record usage for user %s failed", userID)
		return
	}
	if p.invalidator != nil {
		p.invalidator(userID)
	}
	if p.notifier != nil && p.balanceReader != nil {
		if raw, err := p.balanceReader.GetWalletBalance(bgCtx, userID); err == nil {
			if bal, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64); bal <= p.lowBalanceThresh && bal > p.lowBalanceThresh-cost*2 {
				p.notifier.Send(bgCtx, fmt.Sprintf("⚠️ 用户 %s 余额不足: %.2f", userID, bal))
			}
		}
	}
}

// requestIDFromContext picks up a request id if any upstream component placed
// one in the context. We accept any of a small set of conventional keys to
// avoid forcing every caller to import the billing package.
func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	for _, key := range []any{"requestID", "request_id", "X-Request-Id"} {
		if v, ok := ctx.Value(key).(string); ok && v != "" {
			return v
		}
	}
	return ""
}
