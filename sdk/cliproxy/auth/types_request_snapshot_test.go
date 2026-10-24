package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestRequestTokenPartitionSurvivesGinContextReuse(t *testing.T) {
	auth := &Auth{Provider: "codex", Metadata: map[string]any{
		"access_token": "outer-test-token",
		"use_siwc":     true,
		"siwc":         map[string]any{"access_token": "siwc-test-token"},
	}}
	for _, tc := range []struct {
		name      string
		userAgent string
		nextAgent string
		wantToken string
	}{
		{"codex request", "codex_cli_rs/1.0", "browser", "outer-test-token"},
		{"other client", "browser", "codex_cli_rs/1.0", "siwc-test-token"},
		{"empty user agent", "", "codex_cli_rs/1.0", "siwc-test-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			ginCtx.Request.Header.Set("User-Agent", tc.userAgent)
			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			ctx = logging.WithClientRequestMetadata(ctx, logging.ClientRequestMetadata{UserAgent: tc.userAgent})
			// Streaming finalization can outlive the handler and race with context reuse.
			next := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			next.Header.Set("User-Agent", tc.nextAgent)
			ginCtx.Request = next
			if token := authAccessTokenForRequest(ctx, auth); token != tc.wantToken {
				t.Fatalf("request selected token from a recycled context: got %q, want %q", token, tc.wantToken)
			}
			if !IsCodexClientRequest(ctx, http.Header{"User-Agent": {"codex-rs/1.0"}}) {
				t.Fatal("explicit Codex headers must still identify a Codex request")
			}
		})
	}
}

func TestCodexClientDetectionRetainsGinFallbackWithoutSnapshot(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("User-Agent", "codex-rs/1.0")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	if !IsCodexClientRequest(ctx) || IsCodexClientRequest(nil) {
		t.Fatal("request detection without a snapshot changed")
	}
}
