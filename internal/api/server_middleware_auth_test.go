package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type failingAccessProvider struct {
	cause error
}

func (p failingAccessProvider) Identifier() string { return "failing-test-provider" }

func (p failingAccessProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, sdkaccess.NewInternalAuthError("api key lookup failed", p.cause)
}

func TestAuthMiddlewareClassifiesRequestCancellation(t *testing.T) {
	tests := []struct {
		name         string
		cause        error
		status       int
		message      string
		realtime     bool
		realtimeCode string
	}{
		{name: "canceled", cause: context.Canceled, status: clienterror.StatusClientClosedRequest, message: "Request canceled"},
		{name: "deadline", cause: context.DeadlineExceeded, status: http.StatusGatewayTimeout, message: "Authentication request timed out"},
		{name: "realtime canceled", cause: context.Canceled, status: clienterror.StatusClientClosedRequest, message: "Request canceled", realtime: true, realtimeCode: "request_canceled"},
		{name: "realtime deadline", cause: context.DeadlineExceeded, status: http.StatusGatewayTimeout, message: "Authentication request timed out", realtime: true, realtimeCode: "authentication_timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := sdkaccess.NewManager()
			manager.SetProviders([]sdkaccess.Provider{failingAccessProvider{cause: tt.cause}})
			router := gin.New()
			if tt.realtime {
				router.Use(realtimeStandardAuthMiddleware(manager))
			} else {
				router.Use(AuthMiddleware(manager))
			}
			router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tt.status, recorder.Body.String())
			}
			if tt.realtime {
				var response struct {
					Error struct {
						Message string `json:"message"`
						Code    string `json:"code"`
					} `json:"error"`
				}
				if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
					t.Fatalf("decode response: %v", errUnmarshal)
				}
				if response.Error.Message != tt.message || response.Error.Code != tt.realtimeCode {
					t.Fatalf("error = %#v, want message %q and code %q", response.Error, tt.message, tt.realtimeCode)
				}
				return
			}
			var response map[string]string
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
				t.Fatalf("decode response: %v", errUnmarshal)
			}
			if response["error"] != tt.message {
				t.Fatalf("error = %q, want %q", response["error"], tt.message)
			}
		})
	}
}

func TestAuthenticationErrorStatusPreservesRealFailure(t *testing.T) {
	cause := errors.New("database unavailable")
	status, message := authenticationErrorStatus(sdkaccess.NewInternalAuthError("api key lookup failed", cause))
	if status != http.StatusInternalServerError || message != "api key lookup failed" {
		t.Fatalf("status, message = %d, %q", status, message)
	}
}
