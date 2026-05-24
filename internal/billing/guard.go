package billing

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// BalanceReader is the subset of the store used by BalanceGuard.
type BalanceReader interface {
	GetWalletBalance(ctx context.Context, userID string) (string, error)
}

// BalanceGuard rejects requests when the caller's wallet balance is at or
// below the configured threshold. A short in-process cache absorbs request
// bursts so the database is queried at most once per user per cache window.
type BalanceGuard struct {
	store     BalanceReader
	threshold float64
	cacheTTL  time.Duration

	mu    sync.Mutex
	cache map[string]balanceCacheEntry
}

type balanceCacheEntry struct {
	balance   float64
	expiresAt time.Time
}

// NewBalanceGuard constructs a guard. Pass cacheTTL <= 0 to disable caching.
// threshold is the inclusive minimum; balance <= threshold is rejected.
func NewBalanceGuard(store BalanceReader, threshold float64, cacheTTL time.Duration) *BalanceGuard {
	return &BalanceGuard{
		store:     store,
		threshold: threshold,
		cacheTTL:  cacheTTL,
		cache:     make(map[string]balanceCacheEntry),
	}
}

// Handler returns the gin middleware that enforces the policy. Requests from
// non-billing users (no user id on context) pass through untouched so the
// existing config-api-key path keeps working.
func (g *BalanceGuard) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if g == nil || g.store == nil {
			return
		}
		userID := UserIDFromContext(c.Request.Context())
		if userID == "" {
			return
		}
		bal, err := g.balanceFor(c.Request.Context(), userID)
		if err != nil {
			log.WithError(err).Errorf("billing: balance lookup failed for user %s", userID)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "balance lookup failed"})
			return
		}
		if bal <= g.threshold {
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":   "insufficient balance",
				"balance": bal,
			})
			return
		}
	}
}

// Invalidate forces the next request from userID to re-read the balance from
// the store. Called by the metering plugin and topup confirmation paths so a
// recent debit or credit is visible to the guard immediately.
func (g *BalanceGuard) Invalidate(userID string) {
	if g == nil || userID == "" {
		return
	}
	g.mu.Lock()
	delete(g.cache, userID)
	g.mu.Unlock()
}

func (g *BalanceGuard) balanceFor(ctx context.Context, userID string) (float64, error) {
	if g.cacheTTL > 0 {
		g.mu.Lock()
		entry, ok := g.cache[userID]
		g.mu.Unlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry.balance, nil
		}
	}

	raw, err := g.store.GetWalletBalance(ctx, userID)
	if err != nil {
		return 0, err
	}
	bal, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)

	if g.cacheTTL > 0 {
		g.mu.Lock()
		g.cache[userID] = balanceCacheEntry{
			balance:   bal,
			expiresAt: time.Now().Add(g.cacheTTL),
		}
		g.mu.Unlock()
	}
	return bal, nil
}

// RateLimiter applies a per-user token bucket. Tokens refill at rate tokens/s
// up to burst capacity. The implementation is in-memory so multi-replica
// deployments get per-replica limits; move to Redis when horizontal scaling
// arrives.
type RateLimiter struct {
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

// NewRateLimiter constructs a limiter with the given refill rate and burst.
// rate <= 0 or burst <= 0 disables limiting (returns a no-op).
func NewRateLimiter(rate, burst float64) *RateLimiter {
	return &RateLimiter{rate: rate, burst: burst, buckets: make(map[string]*tokenBucket)}
}

// Handler returns the gin middleware. Non-billing users (no user id on
// context) bypass the limiter.
func (l *RateLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if l == nil || l.rate <= 0 || l.burst <= 0 {
			return
		}
		userID := UserIDFromContext(c.Request.Context())
		if userID == "" {
			return
		}
		if !l.allow(userID) {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
	}
}

func (l *RateLimiter) allow(userID string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[userID]
	if !ok {
		b = &tokenBucket{tokens: l.burst, updated: now}
		l.buckets[userID] = b
	}
	elapsed := now.Sub(b.updated).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.updated = now
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// SweepStale removes per-user buckets that have not been touched in maxIdle.
// Call from a background ticker to keep the map bounded.
func (l *RateLimiter) SweepStale(maxIdle time.Duration) {
	if l == nil || maxIdle <= 0 {
		return
	}
	cutoff := time.Now().Add(-maxIdle)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
