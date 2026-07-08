package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/access/db_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules/portal"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// billingEnabled reports whether the operator opted into the paid-tier
// features via the BILLING_ENABLED environment variable.
func billingEnabled() bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("BILLING_ENABLED")))
	return v
}

// setupBilling wires the database-backed access provider and the portal
// module when billing is enabled and a Postgres store is available. It
// returns the additional api.ServerOption values that mount the portal
// routes; an empty slice means billing is disabled or unconfigured.
func setupBilling(ctx context.Context, pg *store.PostgresStore) []api.ServerOption {
	if !billingEnabled() {
		return nil
	}
	if pg == nil {
		log.Warn("BILLING_ENABLED set but Postgres store is not active; billing disabled")
		return nil
	}
	secret := strings.TrimSpace(os.Getenv("BILLING_JWT_SECRET"))
	if secret == "" {
		log.Error("BILLING_ENABLED set but BILLING_JWT_SECRET is empty; billing disabled")
		return nil
	}

	schemaCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := pg.EnsureBillingSchema(schemaCtx); err != nil {
		cancel()
		log.Errorf("failed to ensure billing schema: %v", err)
		return nil
	}
	cancel()

	// Register the DB-backed API key provider so inbound proxy requests can
	// authenticate against the billing api_keys table.
	dbaccess.Register("", pg.LookupAPIKey, pg.TouchAPIKeyLastUsed)

	// Register the metering plugin so token usage is priced and debited.
	pricingPath := strings.TrimSpace(os.Getenv("BILLING_PRICING_FILE"))
	markup, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_MARKUP")), 64)
	pricing, err := billing.LoadPricingFromFile(pricingPath, markup)
	if err != nil {
		log.Errorf("billing pricing load failed: %v", err)
	} else if pricingPath != "" {
		log.Infof("billing pricing loaded from %s (markup=%.2fx)", pricingPath, markup)
	} else {
		log.Warn("BILLING_PRICING_FILE not set; usage will be recorded with zero cost")
	}
	balanceThreshold, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_BALANCE_THRESHOLD")), 64)
	balanceCacheTTL := parseDurationDefault(os.Getenv("BILLING_BALANCE_CACHE_TTL"), 10*time.Second)
	balanceGuard := billing.NewBalanceGuard(pg, balanceThreshold, balanceCacheTTL)

	rate, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_RATE_PER_SEC")), 64)
	burst, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_RATE_BURST")), 64)
	bypassRateLimit, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("BILLING_UNBILLED_BYPASS_RATE_LIMIT")))
	rateLimiter := billing.NewRateLimiter(rate, burst, bypassRateLimit)

	api.RegisterPostAuthHandler(rateLimiter.Handler())
	api.RegisterPostAuthHandler(balanceGuard.Handler())
	// Per-user account restriction (set by the super admin): narrow candidate
	// selection to the allowed set so CPA's own scheduler (round-robin /
	// fill-first) load-balances within it. A single allowed account behaves
	// like a pin; an empty set leaves normal scheduling untouched.
	api.RegisterPostAuthHandler(func(c *gin.Context) {
		reqCtx := c.Request.Context()
		allowed := billing.AllowedAuthIDsFromContext(reqCtx)
		if len(allowed) == 0 {
			return
		}
		c.Request = c.Request.WithContext(sdkhandlers.WithAllowedAuthIDs(reqCtx, allowed))
	})
	// Per-user model restriction (set by the super admin): 403 a request whose
	// target model is not on the user's allowed list.
	api.RegisterPostAuthHandler(billing.ModelAccessGuard())
	go sweepRateLimiter(rateLimiter)

	// Expose the full client-visible model list to the portal (admin's per-user
	// model whitelist picker) via a lazy lister over the global model registry.
	billing.SetModelLister(func() []string {
		entries := registry.GetGlobalRegistry().GetAvailableModels("openai")
		out := make([]string, 0, len(entries))
		seen := make(map[string]struct{}, len(entries))
		for _, e := range entries {
			id, _ := e["id"].(string)
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return out
	})

	meter := billing.NewMeterPlugin(pg, pricing)
	meter.SetInvalidator(balanceGuard.Invalidate)
	usage.RegisterPlugin(meter)

	if adminEmail := strings.TrimSpace(os.Getenv("BILLING_ADMIN_EMAIL")); adminEmail != "" {
		promoteCtx, cancelPromote := context.WithTimeout(ctx, 10*time.Second)
		switch err := pg.PromoteUserToAdmin(promoteCtx, adminEmail); {
		case err == nil:
			log.Infof("billing: promoted %s to admin", adminEmail)
		case errors.Is(err, store.ErrUserNotFound):
			log.Warnf("billing: admin email %s not yet registered; promotion will retry on next restart", adminEmail)
		default:
			log.Errorf("billing: promote admin %s failed: %v", adminEmail, err)
		}
		cancelPromote()
	}

	tokens := billing.NewTokenIssuer(secret, 24*time.Hour)
	topupCfg := billing.LoadTopupConfigFromEnv()
	if len(topupCfg.Methods()) == 0 {
		log.Warn("billing: no top-up methods configured (set BILLING_USDT_TRC20/ERC20/BEP20)")
	} else {
		log.Infof("billing: %d top-up method(s) loaded", len(topupCfg.Methods()))
	}
	notifier := billing.NewTelegramNotifierFromEnv()

	meter.SetLowBalanceNotifier(notifier, pg, balanceThreshold+1)

	module := portal.New(pg, tokens, topupCfg)
	module.SetWalletChangeHook(balanceGuard.Invalidate)
	module.SetNotifier(notifier)

	startUSDTWatcher(ctx, pg, balanceGuard.Invalidate)
	go expireOrdersLoop(pg)

	configurator := func(engine *gin.Engine, baseHandler *sdkhandlers.BaseAPIHandler, _ *config.Config) {
		// Wire the upstream-account lister from the core auth manager so the
		// portal can list accounts for binding and the meter can resolve labels.
		if baseHandler != nil && baseHandler.AuthManager != nil {
			mgr := baseHandler.AuthManager
			billing.SetAccountLister(func() []billing.Account {
				auths := mgr.List()
				out := make([]billing.Account, 0, len(auths))
				for _, a := range auths {
					if a == nil {
						continue
					}
					out = append(out, billing.Account{
						ID:       a.ID,
						Provider: a.Provider,
						Label:    a.Label,
						Status:   string(a.Status),
					})
				}
				return out
			})
		}
		group := engine.Group("/portal")
		module.RegisterRoutes(group)
		log.Info("billing portal routes mounted at /portal")
	}
	return []api.ServerOption{api.WithRouterConfigurator(configurator)}
}

// parseDurationDefault parses a duration string and returns def on empty or
// invalid input.
func parseDurationDefault(raw string, def time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// sweepRateLimiter drops idle per-user buckets every minute so the limiter
// map does not grow unbounded over the lifetime of the process.
func sweepRateLimiter(l *billing.RateLimiter) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.SweepStale(15 * time.Minute)
	}
}

// expireOrdersLoop runs every minute and cancels topup orders past their TTL.
func expireOrdersLoop(pg *store.PostgresStore) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		n, err := pg.ExpirePendingTopupOrders(ctx)
		cancel()
		if err != nil {
			log.WithError(err).Error("billing: expire orders failed")
		} else if n > 0 {
			log.Infof("billing: expired %d stale topup order(s)", n)
		}
	}
}

// startUSDTWatcher spins up the TRC20 auto-confirm poller when the operator
// opted in. The watcher is a no-op when its required env knobs are missing
// (wallet address or auto-confirm toggle) so the admin manual-confirm flow
// remains the default.
func startUSDTWatcher(ctx context.Context, sink billing.TopupConfirmer, invalidate func(userID string)) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("BILLING_USDT_AUTO_CONFIRM")), "true") {
		return
	}
	wallet := strings.TrimSpace(os.Getenv("BILLING_USDT_TRC20"))
	if wallet == "" {
		log.Warn("billing: USDT auto-confirm enabled but BILLING_USDT_TRC20 is empty")
		return
	}
	tolerance, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_USDT_AMOUNT_TOLERANCE")), 64)
	watcher := billing.NewUSDTTronWatcher(sink, billing.USDTTronWatcherConfig{
		WalletAddress:   wallet,
		TronAPIBase:     strings.TrimSpace(os.Getenv("BILLING_TRONGRID_API_BASE")),
		TronAPIKey:      strings.TrimSpace(os.Getenv("BILLING_TRONGRID_API_KEY")),
		PollInterval:    parseDurationDefault(os.Getenv("BILLING_USDT_POLL_INTERVAL"), 30*time.Second),
		AmountTolerance: tolerance,
		OnWalletChange:  invalidate,
	})
	watcher.Start(ctx)
}
