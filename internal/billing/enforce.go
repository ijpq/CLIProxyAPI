package billing

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ModelAccessGuard returns a gin middleware that rejects a request whose target
// model is not in the authenticated user's allowed-models list. Users with no
// model restriction (empty list) and non-billing requests pass through. The
// model is read from the JSON body's "model" field (openai/claude/codex/
// responses dialects), falling back to the URL path for Gemini-style routes.
// When the model cannot be determined the request is allowed (fail-open) so
// unusual endpoints are never hard-blocked.
func ModelAccessGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed := AllowedModelsFromContext(c.Request.Context())
		if len(allowed) == 0 {
			return
		}
		model := requestedModel(c)
		if model == "" {
			return // fail-open: could not determine the model
		}
		for _, m := range allowed {
			if strings.EqualFold(strings.TrimSpace(m), model) {
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "model not allowed for this account",
			"model": model,
		})
	}
}

// requestedModel best-effort extracts the client-requested model name from the
// request, restoring the body so downstream handlers can still read it.
func requestedModel(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if c.Request.Body != nil {
		body, err := io.ReadAll(c.Request.Body)
		_ = c.Request.Body.Close()
		// Always restore the body for downstream handlers.
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if err == nil && len(body) > 0 {
			if m := strings.TrimSpace(gjson.GetBytes(body, "model").String()); m != "" {
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
