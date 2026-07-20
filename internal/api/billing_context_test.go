package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type billingContextAccessProvider struct{}

func (billingContextAccessProvider) Identifier() string { return "billing-context-test" }

func (billingContextAccessProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return &sdkaccess.Result{
		Provider:  "billing-context-test",
		Principal: "user-1",
		Metadata: map[string]string{
			billing.MetadataKeyUserID:         "user-1",
			billing.MetadataKeyAPIKeyID:       "key-1",
			billing.MetadataKeyUnbilled:       "1",
			billing.MetadataKeyAllowedAuthIDs: "account-1,account-2",
			billing.MetadataKeyAllowedModels:  "model-1,model-2",
		},
	}, nil
}

func TestBillingAccessContextReachesExecutionContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accessManager := sdkaccess.NewManager()
	accessManager.SetProviders([]sdkaccess.Provider{billingContextAccessProvider{}})
	baseHandler := apiHandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)

	router := gin.New()
	router.Use(AuthMiddleware(accessManager))
	router.GET("/test", func(c *gin.Context) {
		executionCtx, cancel := baseHandler.GetContextWithCancel(nil, c, context.Background())
		defer cancel()

		if got := billing.UserIDFromContext(executionCtx); got != "user-1" {
			t.Errorf("execution user id = %q, want user-1", got)
		}
		if got := billing.APIKeyIDFromContext(executionCtx); got != "key-1" {
			t.Errorf("execution api key id = %q, want key-1", got)
		}
		if !billing.UnbilledFromContext(executionCtx) {
			t.Error("execution context lost unbilled permission")
		}
		if got := billing.AllowedAuthIDsFromContext(executionCtx); !reflect.DeepEqual(got, []string{"account-1", "account-2"}) {
			t.Errorf("execution allowed account ids = %v", got)
		}
		if got := billing.AllowedModelsFromContext(executionCtx); !reflect.DeepEqual(got, []string{"model-1", "model-2"}) {
			t.Errorf("execution allowed models = %v", got)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer portal-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
