// Package portal exposes the customer-facing HTTP surface for the paid tier:
// account registration and login, API key management, wallet balance, and
// usage history. The package is decoupled from concrete storage by depending
// on the Store interface, which the Postgres-backed billing store
// (internal/store) satisfies.
package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Store is the persistence surface required by the portal handlers. The
// concrete implementation is *store.PostgresStore but the interface lets tests
// substitute a fake.
type Store interface {
	CreateUser(ctx context.Context, email, passwordHash, displayName string) (store.User, error)
	GetUserByEmail(ctx context.Context, email string) (store.User, error)
	GetUserByID(ctx context.Context, id string) (store.User, error)
	CreateAPIKey(ctx context.Context, userID, keyHash, keyPrefix, name string) (store.APIKeyRecord, error)
	ListAPIKeys(ctx context.Context, userID string) ([]store.APIKeyRecord, error)
	RevokeAPIKey(ctx context.Context, userID, keyID string) error
	GetWalletBalance(ctx context.Context, userID string) (string, error)
	ListUsage(ctx context.Context, userID string, before time.Time, limit int) ([]store.UsageRecord, error)

	CreateTopupOrder(ctx context.Context, userID, method, amountStr, currency, network, walletAddress string, ttl time.Duration) (store.TopupOrder, error)
	GetTopupOrder(ctx context.Context, id, userID string) (store.TopupOrder, error)
	ListTopupOrders(ctx context.Context, userID string, limit int) ([]store.TopupOrder, error)
	SubmitTopupTxHash(ctx context.Context, userID, orderID, txHash string) (store.TopupOrder, error)
	CancelTopupOrder(ctx context.Context, userID, orderID string) error
	ConfirmTopupOrder(ctx context.Context, orderID, adminNote string) (store.TopupOrder, error)
}

// Module bundles the portal dependencies and exposes route registration.
type Module struct {
	store           Store
	tokens          *billing.TokenIssuer
	topup           *billing.TopupConfig
	keyGen          func() (raw string, err error)
	onWalletChanged func(userID string)
}

// New builds a portal module. A nil keyGen falls back to the default 32-byte
// random generator with the "cpk_" prefix. A nil topup config disables the
// payment endpoints (they respond 503).
func New(s Store, tokens *billing.TokenIssuer, topup *billing.TopupConfig) *Module {
	return &Module{store: s, tokens: tokens, topup: topup, keyGen: defaultKeyGenerator}
}

// SetWalletChangeHook registers a callback invoked after a wallet-mutating
// operation (top-up confirmation) so downstream caches can be invalidated.
func (m *Module) SetWalletChangeHook(fn func(userID string)) {
	if m == nil {
		return
	}
	m.onWalletChanged = fn
}

// RegisterRoutes mounts the portal endpoints under the provided router group.
// Public routes (register/login) are added without auth; everything else is
// guarded by the JWT middleware.
func (m *Module) RegisterRoutes(r gin.IRouter) {
	if m == nil {
		return
	}
	r.POST("/register", m.handleRegister)
	r.POST("/login", m.handleLogin)

	authed := r.Group("")
	authed.Use(m.AuthMiddleware())
	authed.GET("/me", m.handleMe)
	authed.GET("/wallet", m.handleWallet)
	authed.GET("/usage", m.handleUsage)
	authed.GET("/api-keys", m.handleListKeys)
	authed.POST("/api-keys", m.handleCreateKey)
	authed.DELETE("/api-keys/:id", m.handleRevokeKey)

	authed.GET("/topup/methods", m.handleListTopupMethods)
	authed.POST("/topup", m.handleCreateTopupOrder)
	authed.GET("/topup", m.handleListTopupOrders)
	authed.GET("/topup/:id", m.handleGetTopupOrder)
	authed.POST("/topup/:id/submit", m.handleSubmitTopupTxHash)
	authed.POST("/topup/:id/cancel", m.handleCancelTopupOrder)

	admin := r.Group("/admin")
	admin.Use(m.AuthMiddleware(), m.adminOnly())
	admin.GET("/topup", m.handleAdminListTopupOrders)
	admin.POST("/topup/:id/confirm", m.handleAdminConfirmTopupOrder)
}

// defaultKeyGenerator produces a 32-byte random key encoded as
// "cpk_<64-hex>". The prefix makes leaked keys easy to recognize.
func defaultKeyGenerator() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "cpk_" + hex.EncodeToString(buf[:]), nil
}

// keyPrefix returns the first 12 characters of a raw key for display in lists.
func keyPrefix(raw string) string {
	if len(raw) <= 12 {
		return raw
	}
	return raw[:12]
}
