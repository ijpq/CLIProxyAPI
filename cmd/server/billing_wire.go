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
	usage.RegisterPlugin(billing.NewMeterPlugin(pg, pricing))

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
	module := portal.New(pg, tokens, topupCfg)

	configurator := func(engine *gin.Engine, _ *sdkhandlers.BaseAPIHandler, _ *config.Config) {
		group := engine.Group("/portal")
		module.RegisterRoutes(group)
		log.Info("billing portal routes mounted at /portal")
	}
	return []api.ServerOption{api.WithRouterConfigurator(configurator)}
}
