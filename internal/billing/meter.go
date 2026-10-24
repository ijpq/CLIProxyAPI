package billing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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

// NewMeterPlugin builds the plugin. A nil pricing table uses the request's
// versioned points price. A nil sink disables event storage.
func NewMeterPlugin(sink MeterSink, pricing *PricingTable) *MeterPlugin {
	return &MeterPlugin{sink: sink, pricing: pricing}
}

func (p *MeterPlugin) SynchronousUsage() bool { return true }

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
		// config-api-key, internal management); nothing to meter. Logged at
		// debug so operators can confirm why a request produced no usage row.
		log.WithFields(log.Fields{
			"provider": record.Provider,
			"model":    record.Model,
		}).Debug("billing: skip metering (no billing user in request context)")
		return
	}

	cost := 0.0
	detail := usage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	input, output := record.Detail.InputTokens, record.Detail.OutputTokens
	cacheRead, cacheWrite := record.Detail.CacheReadTokens, record.Detail.CacheCreationTokens
	if b := detail.TokenBreakdown; b.Valid() && b.Quality == usage.TokenAccountingQualityComplete {
		input, output = b.Input.UncachedTokens, b.Output.TotalTokens
		cacheRead, cacheWrite = b.Input.CacheReadTokens, b.Input.CacheWriteTokens
	}
	if p.pricing != nil {
		cost = p.pricing.Cost(
			record.Provider, record.Model,
			input, output, cacheRead, cacheWrite,
		)
	}
	costString := fmt.Sprintf("%.6f", cost)
	var priceVersion int64
	if p.pricing == nil {
		var err error
		costString, priceVersion, err = pointsCost(ctx, input, output, cacheRead, cacheWrite)
		if err != nil {
			log.WithError(err).Error("billing: calculate points charge failed")
			return
		}
		cost, _ = strconv.ParseFloat(costString, 64)
	}

	status := "success"
	errMsg := ""
	if record.Failed {
		status = "error"
		errMsg = fmt.Sprintf("upstream request failed (status %d)", record.Fail.StatusCode)
	}

	unbilled := UnbilledFromContext(ctx)
	authID := strings.TrimSpace(record.AuthID)
	authLabel := ""
	if authID != "" {
		authLabel = AccountLabel(authID)
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requestID := strings.TrimSpace(record.RequestID)
	if requestID == "" {
		requestID = requestIDFromContext(ctx)
	}
	eventID := strings.TrimSpace(record.EventID)
	if eventID == "" {
		eventID = uuid.NewString()
	}

	if err := p.sink.RecordUsageAndDebit(bgCtx, store.MeteredUsage{
		EventID:          eventID,
		UserID:           userID,
		APIKeyID:         APIKeyIDFromContext(ctx),
		RequestID:        requestID,
		Provider:         record.Provider,
		Model:            record.Model,
		InputTokens:      record.Detail.InputTokens,
		OutputTokens:     record.Detail.OutputTokens,
		CacheReadTokens:  record.Detail.CacheReadTokens,
		CacheWriteTokens: record.Detail.CacheCreationTokens,
		Cost:             costString,
		PriceVersion:     priceVersion,
		Status:           status,
		ErrorMessage:     errMsg,
		AuthID:           authID,
		AuthLabel:        authLabel,
		SkipDebit:        unbilled,
	}); err != nil {
		log.WithError(err).Errorf("billing: record usage for user %s failed", userID)
		return
	}
	// Audit trail (every metered request): which user/key drew from which
	// upstream account, and whether it was billed. Also the definitive signal
	// that a usage row was written.
	log.WithFields(log.Fields{
		"user":     userID,
		"account":  authID,
		"label":    authLabel,
		"provider": record.Provider,
		"model":    record.Model,
		"cost":     fmt.Sprintf("%.6f", cost),
		"unbilled": unbilled,
	}).Info("billing: request metered")
	if p.invalidator != nil {
		p.invalidator(userID)
	}
	if !unbilled && p.notifier != nil && p.balanceReader != nil {
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
