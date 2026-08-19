package billing

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// ModelAccessGuard returns a gin middleware that rejects a request whose target
// model is not in the authenticated user's allowed-models list. Users with no
// model restriction (empty list) and non-billing requests pass through. The
// model is read from the JSON body's "model" field (openai/claude/codex/
// responses dialects), falling back to the URL path for Gemini-style routes.
// When the model cannot be determined this early guard allows the request to
// continue. The execution layer performs the authoritative check after the
// request model has been resolved, including WebSocket and plugin routes.
func ModelAccessGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed := AllowedModelsFromContext(c.Request.Context())
		if len(allowed) == 0 {
			return
		}
		model := requestedModel(c)
		if model == "" {
			return // The execution layer validates the resolved model.
		}
		if !ModelAllowed(c.Request.Context(), model) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "model not allowed for this account",
				"model": model,
			})
		}
	}
}

// ModelAllowed reports whether model is permitted by the billing access state
// attached to ctx. An empty allowlist is unrestricted; a restricted request with
// no resolved model fails closed.
func ModelAllowed(ctx context.Context, model string) bool {
	allowed := AllowedModelsFromContext(ctx)
	if len(allowed) == 0 {
		return true
	}
	requestedModel := normalizeAccessModel(model)
	if requestedModel == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.EqualFold(normalizeAccessModel(candidate), requestedModel) {
			return true
		}
	}
	return false
}

// AuthAllowed reports whether authID is permitted by the billing access state
// attached to ctx. An empty allowlist is unrestricted; a restricted request with
// no selected auth identity fails closed.
func AuthAllowed(ctx context.Context, authID string) bool {
	allowed := AllowedAuthIDsFromContext(ctx)
	if len(allowed) == 0 {
		return true
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.TrimSpace(candidate) == authID {
			return true
		}
	}
	return false
}

// ExecutionAccessMetadata copies base and adds the request's billing access
// restrictions for auth selection and client-visible model validation.
func ExecutionAccessMetadata(ctx context.Context, base map[string]any, requestedModel string) map[string]any {
	var metadata map[string]any
	if len(base) > 0 {
		metadata = make(map[string]any, len(base)+3)
		for key, value := range base {
			metadata[key] = value
		}
	}
	set := func(key string, value any) {
		if metadata == nil {
			metadata = make(map[string]any, 3)
		}
		metadata[key] = value
	}
	if model := strings.TrimSpace(requestedModel); model != "" {
		set(coreexecutor.RequestedModelMetadataKey, model)
	}
	if allowed := AllowedAuthIDsFromContext(ctx); len(allowed) > 0 {
		set(coreexecutor.AllowedAuthIDsMetadataKey, append([]string(nil), allowed...))
	}
	if allowed := AllowedModelsFromContext(ctx); len(allowed) > 0 {
		set(coreexecutor.AllowedModelsMetadataKey, append([]string(nil), allowed...))
	}
	return metadata
}

func normalizeAccessModel(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
}

const requestedModelInspectionLimit = 64 << 10

type replayRequestBody struct {
	io.Reader
	io.Closer
}

// requestedModel best-effort extracts the client-requested model name from a
// bounded body prefix, restoring the prefix and unread stream for downstream
// handlers. Route handlers remain responsible for authoritative body limits.
func requestedModel(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if c.Request.Body != nil {
		originalBody := c.Request.Body
		prefix, errRead := io.ReadAll(io.LimitReader(originalBody, requestedModelInspectionLimit))
		c.Request.Body = &replayRequestBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), originalBody),
			Closer: originalBody,
		}
		if errRead == nil && len(prefix) > 0 {
			if m := strings.TrimSpace(gjson.GetBytes(prefix, "model").String()); m != "" {
				return m
			}
		}
	}
	// Gemini-style path: /.../models/{model}:generateContent
	if idx := strings.LastIndex(c.Request.URL.Path, "/models/"); idx >= 0 {
		rest := strings.TrimSpace(c.Request.URL.Path[idx+len("/models/"):])
		if colon := strings.IndexByte(rest, ':'); colon >= 0 {
			rest = rest[:colon]
		}
		if rest != "" {
			return rest
		}
	}
	return ""
}
