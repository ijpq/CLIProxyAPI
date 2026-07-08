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
	// MetadataKeyUnbilled is "1" when the authenticating API key is exempt from
	// wallet balance/debit. It is a per-key property, independent of binding.
	MetadataKeyUnbilled = "billing_unbilled"
	// MetadataKeyBoundAuthIDs carries the comma-separated upstream account IDs
	// bound to the authenticating API key.
	MetadataKeyBoundAuthIDs = "billing_bound_auth_ids"
)

type contextKey int

const (
	ctxUserID contextKey = iota + 1
	ctxAPIKeyID
	ctxUnbilled
	ctxBoundAuthIDs
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

// WithUnbilled marks the context as belonging to an unbilled API key (exempt
// from wallet balance/debit).
func WithUnbilled(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxUnbilled, true)
}

// UnbilledFromContext reports whether the request was authenticated with an
// unbilled API key.
func UnbilledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(ctxUnbilled).(bool)
	return v
}

// WithBoundAuthIDs attaches the upstream account IDs bound to the caller's API
// key. An empty slice leaves the context unchanged.
func WithBoundAuthIDs(ctx context.Context, ids []string) context.Context {
	if len(ids) == 0 {
		return ctx
	}
	return context.WithValue(ctx, ctxBoundAuthIDs, ids)
}

// BoundAuthIDsFromContext returns the bound upstream account IDs, or nil.
func BoundAuthIDsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(ctxBoundAuthIDs).([]string)
	return v
}
