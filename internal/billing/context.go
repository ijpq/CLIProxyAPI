// Package billing carries cross-package primitives shared by the paid-tier
// modules: API key lookup metadata keys for the access layer, request-context
// helpers so handlers can attribute usage to a user and enforce per-user access
// controls, and similar small building blocks. Heavier components (pricing
// tables, metering hooks, wallet transactions) live in dedicated files
// alongside this one.
package billing

import "context"

// Metadata keys placed on access.Result.Metadata by the DB-backed API key
// provider. Downstream code reads these to attribute usage and enforce the
// per-user access controls set by the super admin.
const (
	MetadataKeyUserID   = "billing_user_id"
	MetadataKeyAPIKeyID = "billing_api_key_id"
	// MetadataKeyUnbilled is "1" when the owning user is exempt from wallet
	// balance/debit.
	MetadataKeyUnbilled = "billing_unbilled"
	// MetadataKeyAllowedAuthIDs carries the comma-separated upstream account
	// IDs the user is restricted to (empty/absent = all).
	MetadataKeyAllowedAuthIDs = "billing_allowed_auth_ids"
	// MetadataKeyAllowedModels carries the comma-separated client-visible model
	// names the user is restricted to (empty/absent = all).
	MetadataKeyAllowedModels = "billing_allowed_models"
)

type contextKey int

const (
	ctxUserID contextKey = iota + 1
	ctxAPIKeyID
	ctxUnbilled
	ctxAllowedAuthIDs
	ctxAllowedModels
)

// WithUserID returns a derived context carrying the authenticated user id.
func WithUserID(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxUserID, userID)
}

// UserIDFromContext returns the user id attached by WithUserID, or empty.
func UserIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// WithAPIKeyID returns a derived context carrying the authenticated api key id.
func WithAPIKeyID(ctx context.Context, keyID string) context.Context {
	if keyID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxAPIKeyID, keyID)
}

// APIKeyIDFromContext returns the key id attached by WithAPIKeyID, or empty.
func APIKeyIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxAPIKeyID).(string)
	return v
}

// WithUnbilled marks the context as belonging to an unbilled user (exempt from
// wallet balance/debit).
func WithUnbilled(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxUnbilled, true)
}

// UnbilledFromContext reports whether the request was authenticated by an
// unbilled user.
func UnbilledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxUnbilled).(bool)
	return v
}

// WithAllowedAuthIDs attaches the upstream account IDs the user is restricted
// to. An empty slice leaves the context unchanged.
func WithAllowedAuthIDs(ctx context.Context, ids []string) context.Context {
	if len(ids) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxAllowedAuthIDs, ids)
}

// AllowedAuthIDsFromContext returns the allowed upstream account IDs, or nil.
func AllowedAuthIDsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(ctxAllowedAuthIDs).([]string)
	return v
}

// WithAllowedModels attaches the client-visible model names the user is
// restricted to. An empty slice leaves the context unchanged.
func WithAllowedModels(ctx context.Context, models []string) context.Context {
	if len(models) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxAllowedModels, models)
}

// AllowedModelsFromContext returns the allowed model names, or nil.
func AllowedModelsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(ctxAllowedModels).([]string)
	return v
}
