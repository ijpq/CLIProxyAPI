package billing

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestModelAccessGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		allowed     []string
		wantStatus  int
		wantHandler bool
	}{
		{name: "allowed JSON model", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-a"}`, allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "allowed thinking suffix", method: http.MethodPost, path: "/v1/responses", body: `{"model":"model-a(high)"}`, allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "forbidden JSON model", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-b"}`, allowed: []string{"model-a"}, wantStatus: http.StatusForbidden},
		{name: "allowed Gemini path model", method: http.MethodPost, path: "/v1beta/models/model-a:generateContent", allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "forbidden Gemini path model", method: http.MethodPost, path: "/v1beta/models/model-b:generateContent", allowed: []string{"model-a"}, wantStatus: http.StatusForbidden},
		{name: "unrestricted", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"model-b"}`, wantStatus: http.StatusNoContent, wantHandler: true},
		{name: "model resolved later", method: http.MethodGet, path: "/v1/responses", allowed: []string{"model-a"}, wantStatus: http.StatusNoContent, wantHandler: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled := false
			router := gin.New()
			router.Use(func(c *gin.Context) {
				ctx := WithAllowedModels(c.Request.Context(), tt.allowed)
				c.Request = c.Request.WithContext(ctx)
			}, ModelAccessGuard())
			router.Any("/*path", func(c *gin.Context) {
				handled = true
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			if handled != tt.wantHandler {
				t.Fatalf("downstream handled = %t, want %t", handled, tt.wantHandler)
			}
		})
	}
}

func TestRequestedModelRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-a","input":"hello"}`))

	if got := requestedModel(ctx); got != "model-a" {
		t.Fatalf("requestedModel() = %q, want model-a", got)
	}
	if got := requestedModel(ctx); got != "model-a" {
		t.Fatalf("requestedModel() after body restore = %q, want model-a", got)
	}
}
