package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access/db_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/modules/portal"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	codexlive "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// billingEnabled reports whether the operator opted into the paid-tier
// features via the BILLING_ENABLED environment variable.
func billingEnabled() bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("BILLING_ENABLED")))
	return v
}

// resolveBillingStore picks the Postgres database billing uses.
//
// A dedicated BILLING_DATABASE_URL gives billing its OWN database and does NOT
// touch auth/config storage — file-based auths and config.yaml keep working
// exactly as before (billing only creates its own tables). This is the
// recommended setup. When BILLING_DATABASE_URL is unset, billing falls back to
// the shared PGSTORE_DSN store if present (legacy behavior, where Postgres also
// takes over auth/config storage). An enabled but unavailable billing database
// is a startup error rather than a request to disable billing.
func resolveBillingStore(ctx context.Context, sharedPG *store.PostgresStore) (*store.PostgresStore, error) {
	dsn := strings.TrimSpace(os.Getenv("BILLING_DATABASE_URL"))
	if dsn == "" {
		if sharedPG != nil {
			return sharedPG, nil
		}
		return nil, errors.New("billing: set BILLING_DATABASE_URL or PGSTORE_DSN when BILLING_ENABLED is true")
	}
	// Dedicated billing DB: a bare connection + an unused spool dir. We never
	// call Bootstrap/RegisterTokenStore/EnsureSchema on it, so it only ever
	// holds the billing tables and never becomes the auth/config token store.
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:      dsn,
		SpoolDir: filepath.Join(os.TempDir(), "cliproxy-billing-store"),
	})
	if err != nil {
		// Driver parse errors can contain the original DSN, including passwords.
		return nil, errors.New("billing: cannot connect to BILLING_DATABASE_URL; check the database configuration and availability")
	}
	log.Info("billing using dedicated database (BILLING_DATABASE_URL); auth/config storage unchanged")
	return pg, nil
}

func billingModelCatalog(entries []map[string]any) []string {
	additional := codexlive.ClientVisibleModels()
	models := make([]string, 0, len(entries)+len(additional))
	seen := make(map[string]struct{}, len(entries)+len(additional))
	appendModel := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	for _, entry := range entries {
		model, _ := entry["id"].(string)
		appendModel(model)
	}
	if len(models) == 0 {
		return nil
	}
	for _, model := range additional {
		appendModel(model)
	}
	return models
}

// setupBilling wires the database-backed access provider and the portal module
// when billing is enabled. It returns the additional api.ServerOption values
// that mount the portal routes, or an error that must prevent server startup.
// An empty slice without an error means billing was disabled. The passed store is the
// optional shared PGSTORE_DSN store used only as a legacy fallback.
func setupBilling(ctx context.Context, sharedPG *store.PostgresStore) ([]api.ServerOption, error) {
	if !billingEnabled() {
		return nil, nil
	}
	sitePolicy, err := portal.NewSitePolicy(os.Getenv("BILLING_PUBLIC_PORTAL_HOST"), os.Getenv("BILLING_PRIVILEGED_PORTAL_HOST"), os.Getenv("BILLING_PRIVILEGED_USER_IDS"))
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("BILLING_USDT_AUTO_CONFIRM")), "true") {
		return nil, errors.New("billing: shared-address USDT auto-confirm cannot verify payment ownership; disable it and reconcile payments manually")
	}
	secret := strings.TrimSpace(os.Getenv("BILLING_JWT_SECRET"))
	if secret == "" {
		return nil, errors.New("billing: BILLING_JWT_SECRET is required when BILLING_ENABLED is true")
	}
	adminID := strings.TrimSpace(os.Getenv("BILLING_ADMIN_USER_ID"))
	if adminID == "" && strings.TrimSpace(os.Getenv("BILLING_ADMIN_EMAIL")) != "" {
		return nil, errors.New("billing: email-only administrator bootstrap is unsafe; verify the account and configure BILLING_ADMIN_USER_ID")
	}
	if adminID != "" {
		if _, err := uuid.Parse(adminID); err != nil {
			return nil, errors.New("billing: BILLING_ADMIN_USER_ID must be an explicitly approved UUID")
		}
	}
	// Validate pricing before touching the database or registering global hooks.
	pricingPath := strings.TrimSpace(os.Getenv("BILLING_PRICING_FILE"))
	if pricingPath != "" {
		return nil, errors.New("billing: pricing initialization: monetary pricing files must be reconciled; configure points prices in Portal administration")
	}
	pg, err := resolveBillingStore(ctx, sharedPG)
	if err != nil {
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized && pg != sharedPG {
			if errClose := pg.Close(); errClose != nil {
				log.Error("billing: close database after initialization failure")
			}
		}
	}()

	schemaCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := pg.EnsureBillingSchema(schemaCtx); err != nil {
		cancel()
		return nil, fmt.Errorf("billing: ensure schema: %w", err)
	}
	cancel()
	journal, err := billing.NewDurableMeterSink(pg, strings.TrimSpace(os.Getenv("BILLING_EVENT_DIR")))
	if err != nil {
		return nil, errors.New("billing: durable accounting initialization failed; configure a writable persistent BILLING_EVENT_DIR")
	}
	if err = journal.Replay(ctx); err != nil {
		return nil, errors.New("billing: pending accounting events require recovery before startup")
	}
	if adminID != "" {
		if err = pg.PromoteUserIDToAdmin(ctx, adminID); err != nil {
			return nil, errors.New("billing: the approved administrator account is unavailable; verify BILLING_ADMIN_USER_ID")
		}
	}

	// Register the DB-backed API key provider so inbound proxy requests can
	// authenticate against the billing api_keys table.
	dbaccess.RegisterWithRevalidation("", pg.LookupAPIKey, pg.LookupAPIKeyByID, pg.TouchAPIKeyLastUsed)

	// Register the metering plugin so token usage is priced and debited.
	log.Info("billing: using versioned Portal points pricing; unpriced paid models are disabled")
	balanceThreshold, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_BALANCE_THRESHOLD")), 64)
	balanceCacheTTL := parseDurationDefault(os.Getenv("BILLING_BALANCE_CACHE_TTL"), 10*time.Second)
	balanceGuard := billing.NewBalanceGuard(pg, balanceThreshold, balanceCacheTTL)

	rate, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_RATE_PER_SEC")), 64)
	burst, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("BILLING_RATE_BURST")), 64)
	bypassRateLimit, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("BILLING_UNBILLED_BYPASS_RATE_LIMIT")))
	rateLimiter := billing.NewRateLimiter(rate, burst, bypassRateLimit)

	api.RegisterPostAuthHandler(rateLimiter.Handler())
	api.RegisterPostAuthHandler(journal.Guard())
	api.RegisterPostAuthHandler(balanceGuard.Handler())
	api.RegisterPostAuthHandler(billing.PointPriceGuard(pg))
	// Per-user account restriction (set by the super admin): narrow candidate
	// selection to the allowed set so CPA's own scheduler (round-robin /
	// fill-first) load-balances within it. A single allowed account behaves
	// like a pin; an empty set leaves normal scheduling untouched.
	api.RegisterPostAuthHandler(func(c *gin.Context) {
		reqCtx := c.Request.Context()
		executionCtx := reqCtx
		if allowed := billing.AllowedAuthIDsFromContext(reqCtx); len(allowed) > 0 {
			executionCtx = sdkhandlers.WithAllowedAuthIDs(executionCtx, allowed)
		}
		if allowed := billing.AllowedModelsFromContext(reqCtx); allowed != nil {
			executionCtx = sdkhandlers.WithAllowedModels(executionCtx, allowed)
		}
		if executionCtx != reqCtx {
			c.Request = c.Request.WithContext(executionCtx)
		}
	})
	// Reject obvious HTTP model violations early. The execution layer performs
	// the authoritative check once the effective model is known.
	api.RegisterPostAuthHandler(billing.ModelAccessGuard())
	go sweepRateLimiter(rateLimiter)

	// Expose the full client-visible model list to the portal (admin's per-user
	// model whitelist picker) via a lazy lister over the global model registry.
	billing.SetModelLister(func() []string {
		return billingModelCatalog(registry.GetGlobalRegistry().GetAvailableModels("openai"))
	})

	meter := billing.NewMeterPlugin(journal, nil)
	meter.SetInvalidator(balanceGuard.Invalidate)
	journal.SetReplayHook(balanceGuard.Invalidate)
	usage.RegisterPlugin(meter)
	go replayBillingEvents(ctx, journal)

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
	module.SetSitePolicy(sitePolicy)
	module.SetWalletChangeHook(balanceGuard.Invalidate)
	module.SetNotifier(notifier)

	// Passwordless email-code login (enabled only when SMTP is configured).
	if emailSender := billing.NewEmailSenderFromEnv(); emailSender != nil {
		ttl := parseDurationDefault(os.Getenv("BILLING_LOGIN_CODE_TTL"), 10*time.Minute)
		cooldown := parseDurationDefault(os.Getenv("BILLING_LOGIN_CODE_COOLDOWN"), 60*time.Second)
		module.SetEmailLogin(emailSender, billing.NewLoginCodeStore(ttl, cooldown))
		log.Info("billing: email-code login enabled (BILLING_SMTP_HOST configured)")
	} else {
		log.Info("billing: email-code login disabled (set BILLING_SMTP_HOST to enable)")
	}

	startUSDTWatcher(ctx, pg, balanceGuard.Invalidate)
	go expireOrdersLoop(pg)

	configurator := func(engine *gin.Engine, baseHandler *sdkhandlers.BaseAPIHandler, _ *config.Config) {
		if baseHandler != nil && baseHandler.AuthManager != nil {
			module.SetHelper(func(ctx context.Context, model string, messages []portal.ChatMessage, maxOutput int) (string, error) {
				payload, err := json.Marshal(map[string]any{"model": model, "messages": messages, "max_tokens": maxOutput, "stream": false, "n": 1})
				if err != nil {
					return "", err
				}
				ctx = billing.WithUnbilled(ctx)
				ctx = sdkhandlers.WithAllowedModels(ctx, []string{model})
				body, _, failure := baseHandler.ExecuteWithAuthManager(ctx, "openai", model, payload, "")
				if failure != nil {
					return "", errors.New("assistant provider failed")
				}
				reply := gjson.GetBytes(body, "choices.0.message.content")
				if reply.Type != gjson.String || strings.TrimSpace(reply.String()) == "" {
					return "", errors.New("assistant returned no text")
				}
				return reply.String(), nil
			})
		}
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
			// Resolve an account's live bearer token (and Antigravity credits) so
			// the portal 额度 page can query each upstream's own quota API.
			billing.SetAuthResolver(func(id string) (billing.UpstreamAuth, bool) {
				id = strings.TrimSpace(id)
				if id == "" {
					return billing.UpstreamAuth{}, false
				}
				for _, a := range mgr.List() {
					if a == nil || a.ID != id {
						continue
					}
					ua := billing.UpstreamAuth{
						ID:       a.ID,
						Provider: a.Provider,
						ProxyURL: a.ProxyURL,
						Token:    metadataAccessToken(a.Metadata),
					}
					if strings.EqualFold(strings.TrimSpace(a.Provider), "antigravity") {
						if hint, ok := coreauth.GetAntigravityCreditsHint(a.ID); ok {
							ua.AntigravityCredits = &billing.AntigravityCredits{
								Known:           hint.Known,
								Available:       hint.Available,
								CreditAmount:    hint.CreditAmount,
								MinCreditAmount: hint.MinCreditAmount,
								UpdatedAt:       hint.UpdatedAt,
							}
						}
					}
					return ua, true
				}
				return billing.UpstreamAuth{}, false
			})
		}
		group := engine.Group("/portal")
		module.RegisterRoutes(group)
		log.Info("billing portal routes mounted at /portal")
	}
	initialized = true
	return []api.ServerOption{api.WithRouterConfigurator(configurator)}, nil
}

func replayBillingEvents(ctx context.Context, journal *billing.DurableMeterSink) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			replayCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := journal.Replay(replayCtx)
			cancel()
			if err != nil {
				log.Error("billing: pending accounting recovery failed; paid requests remain paused")
			}
		}
	}
}

// metadataAccessToken extracts the live bearer access token from a credential's
// metadata, matching the precedence used by the management APICall ($TOKEN$).
func metadataAccessToken(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	for _, key := range []string{"access_token", "accessToken", "id_token"} {
		if v, ok := metadata[key].(string); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v
			}
		}
	}
	if tokenRaw, ok := metadata["token"]; ok {
		switch typed := tokenRaw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		case map[string]any:
			if v, ok := typed["access_token"].(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
			if v, ok := typed["accessToken"].(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
		}
	}
	return ""
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
