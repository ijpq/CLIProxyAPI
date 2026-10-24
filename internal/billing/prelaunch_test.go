package billing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestMeterCanonicalTokenPricing(t *testing.T) {
	for _, tc := range []struct {
		provider string
		detail   usage.Detail
	}{
		{"openai", usage.Detail{InputTokens: 100, CacheReadTokens: 80, OutputTokens: 10}},
		{"claude", usage.Detail{InputTokens: 20, CacheReadTokens: 80, OutputTokens: 10}},
		{"gemini", usage.Detail{InputTokens: 100, CacheReadTokens: 80, OutputTokens: 5, ReasoningTokens: 5}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			prices := NewPricingTable(1)
			prices.prices["test-model"] = ModelPrice{InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1, CacheWritePerMillion: 2}
			sink := &captureMeterSink{}
			meter := NewMeterPlugin(sink, prices)
			meter.HandleUsage(WithUserID(context.Background(), "test-user"), usage.Record{Provider: tc.provider, Model: "test-model", Detail: tc.detail, RequestID: "execution-test"})
			if len(sink.records) != 1 {
				t.Fatalf("records=%d", len(sink.records))
			}
			if sink.records[0].Cost != "0.000480" {
				t.Errorf("charge=%s, want 0.000480", sink.records[0].Cost)
			}
			if sink.records[0].RequestID != "execution-test" {
				t.Errorf("lost execution request ID: %q", sink.records[0].RequestID)
			}
		})
	}
}

func TestPricingCacheFallbackAndFiniteMarkup(t *testing.T) {
	for _, markup := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		table := NewPricingTable(markup)
		table.prices["model"] = ModelPrice{InputPerMillion: 10}
		if got := table.Cost("", "model", 20, 0, 80, 0); math.Abs(got-0.001) > 1e-12 || math.IsNaN(got) {
			t.Errorf("markup %v: cost=%v, want 0.001", markup, got)
		}
	}
}

func TestPricingRejectsInvalidExplicitFiles(t *testing.T) {
	if _, err := LoadPricingFromFile(filepath.Join(t.TempDir(), "missing.json"), 1); err == nil {
		t.Error("an explicit missing pricing file must fail")
	}
	tests := []string{`{"model":{"input_per_million":-1}}`, `{" MODEL ":{},"model":{}}`}
	for _, body := range tests {
		p := filepath.Join(t.TempDir(), "pricing.json")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPricingFromFile(p, 1); err == nil {
			t.Errorf("accepted invalid pricing: %s", body)
		}
	}
}

type controlledBalanceReader struct {
	mu               sync.Mutex
	value            string
	started, release chan struct{}
	calls            atomic.Int32
}

func (r *controlledBalanceReader) GetWalletBalance(context.Context, string) (string, error) {
	r.mu.Lock()
	value := r.value
	r.mu.Unlock()
	if r.calls.Add(1) == 1 && r.started != nil {
		close(r.started)
		<-r.release
	}
	return value, nil
}

func TestBalanceGuardRejectsInvalidNumericBalances(t *testing.T) {
	for _, balance := range []string{"NaN", "Infinity", "-Infinity", "not-a-balance", ""} {
		t.Run(balance, func(t *testing.T) {
			guard := NewBalanceGuard(&controlledBalanceReader{value: balance}, 0, time.Minute)
			r := gin.New()
			r.Use(func(c *gin.Context) { c.Request = c.Request.WithContext(WithUserID(c.Request.Context(), "user")) }, guard.Handler())
			r.GET("/", func(c *gin.Context) { c.Status(200) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != 503 {
				t.Fatalf("balance=%q, status=%d, want 503", balance, w.Code)
			}
		})
	}
}

func TestBalanceInvalidationDoesNotCacheAnInflightStaleRead(t *testing.T) {
	r := &controlledBalanceReader{value: "10", started: make(chan struct{}), release: make(chan struct{})}
	g := NewBalanceGuard(r, 0, time.Hour)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = g.balanceFor(context.Background(), "user") }()
	<-r.started
	r.mu.Lock()
	r.value = "0"
	r.mu.Unlock()
	g.Invalidate("user")
	close(r.release)
	<-done
	got, err := g.balanceFor(context.Background(), "user")
	if err != nil || got != 0 {
		t.Fatalf("stale balance survived invalidation: %v %v", got, err)
	}
}

func TestPortalTokenRequiresExpiration(t *testing.T) {
	const secret = "disposable-token-test-secret"
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, TokenClaims{UserID: "test-user"}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTokenIssuer(secret, time.Hour).Parse(raw); err == nil {
		t.Fatal("accepted a session without expiration")
	}
}

type watcherTestStore struct{ confirmed int }

func (*watcherTestStore) ListSubmittedTopupOrders(context.Context, string, string, int) ([]store.TopupOrder, error) {
	return nil, nil
}
func (s *watcherTestStore) ConfirmTopupOrder(_ context.Context, id, note string) (store.TopupOrder, error) {
	s.confirmed++
	return store.TopupOrder{ID: id, UserID: "test-user"}, nil
}

func TestUSDTWatcherRequestsConfirmedTransfers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("only_confirmed") != "true" {
			t.Error("watcher includes unconfirmed blockchain transfers")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	w := NewUSDTTronWatcher(&watcherTestStore{}, USDTTronWatcherConfig{WalletAddress: "TTest", TronAPIBase: srv.URL})
	if _, err := w.fetchRecentTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUSDTWatcherRejectsOldOrDifferentRecipientTransfers(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, to  string
		timestamp time.Time
		want      int
	}{
		{"correct transfer", "TAbCWallet", now, 1},
		{"different case base58 address", "TabcWallet", now, 0},
		{"transfer before order", "TAbCWallet", now.Add(-time.Hour), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &watcherTestStore{}
			w := NewUSDTTronWatcher(s, USDTTronWatcherConfig{})
			o := store.TopupOrder{ID: "test-order", UserID: "test-user", Amount: "10", TxHash: "test-hash", WalletAddress: "TAbCWallet", CreatedAt: now.Add(-time.Minute)}
			w.tryConfirm(context.Background(), o, map[string]tronTransfer{"test-hash": {TxHash: "test-hash", To: tc.to, AmountInTokens: 10, Timestamp: tc.timestamp}})
			if s.confirmed != tc.want {
				t.Fatalf("confirmations=%d, want %d", s.confirmed, tc.want)
			}
		})
	}
}

func TestLoginCodeSingleUseExpiryAndAttemptLimit(t *testing.T) {
	s := NewLoginCodeStore(time.Minute, time.Minute)
	code, err := s.Generate("USER@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate("user@example.test"); err != ErrCodeCooldown {
		t.Fatalf("cooldown: %v", err)
	}
	if !s.Verify("user@example.test", code) || s.Verify("user@example.test", code) {
		t.Fatal("code is not single-use")
	}
	code, _ = s.Generate("expired@example.test")
	s.entries["expired@example.test"].expiresAt = time.Now().Add(-time.Second)
	if s.Verify("expired@example.test", code) {
		t.Fatal("expired code accepted")
	}
	code, _ = s.Generate("attempts@example.test")
	for range 5 {
		if s.Verify("attempts@example.test", "invalid") {
			t.Fatal("invalid code accepted")
		}
	}
	if s.Verify("attempts@example.test", code) {
		t.Fatal("attempt limit bypassed")
	}
}
