// Package billing carries cross-package primitives shared by the paid-tier
// modules: API key lookup metadata keys for the access layer, request-context
// helpers so handlers can attribute usage to a user, and similar small
// building blocks. Heavier components (pricing tables, metering hooks, wallet
// transactions) live in dedicated files alongside this one as they are
// implemented.
package billing

import "context"

// Metadata keys placed on access.Result.Metadata by the DB-backed API key
// provider. Downstream metering code reads these to attribute usage and cost
// to the owning user and API key.
const (
	MetadataKeyUserID   = "billing_user_id"
	MetadataKeyAPIKeyID = "billing_api_key_id"
)

type contextKey int

const (
	ctxUserID contextKey = iota + 1
	ctxAPIKeyID
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
