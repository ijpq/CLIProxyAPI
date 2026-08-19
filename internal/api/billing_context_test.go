package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type billingContextAccessProvider struct{}

type realtimeRevalidatingProvider struct {
	revoked bool
}

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

func (*realtimeRevalidatingProvider) Identifier() string { return "billing-context-test" }

func (*realtimeRevalidatingProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, sdkaccess.NewNotHandledError()
}

func (p *realtimeRevalidatingProvider) Revalidate(_ context.Context, previous *sdkaccess.Result) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p.revoked {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	if previous == nil || previous.Principal != "issuer-key" || previous.Metadata[billing.MetadataKeyAPIKeyID] != "key-1" || previous.Metadata["tenant"] != "tenant-1" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{
		Provider:  p.Identifier(),
		Principal: "issuer-key",
		Metadata: map[string]string{
			"tenant":                          "tenant-current",
			billing.MetadataKeyUserID:         "user-1",
			billing.MetadataKeyAPIKeyID:       "key-1",
			billing.MetadataKeyAllowedAuthIDs: "account-current",
			billing.MetadataKeyAllowedModels:  "model-1,model-current",
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

func TestRealtimeClientSecretRestoresBillingContextAndRunsPostAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := codexlive.NewHandler(auth.NewManager(nil, nil, nil), nil)
	t.Cleanup(handler.Close)
	revalidator := &realtimeRevalidatingProvider{}
	accessManager := sdkaccess.NewManager()
	accessManager.SetProviders([]sdkaccess.Provider{revalidator})

	issuer := gin.New()
	issuer.POST("/v1/realtime/client_secrets", func(c *gin.Context) {
		c.Set("userApiKey", "issuer-key")
		c.Set("accessProvider", "billing-context-test")
		c.Set("accessMetadata", map[string]string{"tenant": "tenant-1"})
		ctx := billing.WithUserID(c.Request.Context(), "user-1")
		ctx = billing.WithAPIKeyID(ctx, "key-1")
		ctx = billing.WithUnbilled(ctx)
		ctx = billing.WithAllowedAuthIDs(ctx, []string{"account-1", "account-2"})
		ctx = billing.WithAllowedModels(ctx, []string{"model-1", "model-2"})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, handler.CreateClientSecret)
	issueRequest := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime","model":"model-1"}}`))
	issueResponse := httptest.NewRecorder()
	issuer.ServeHTTP(issueResponse, issueRequest)
	if issueResponse.Code != http.StatusOK {
		t.Fatalf("issue status = %d, want %d; body=%s", issueResponse.Code, http.StatusOK, issueResponse.Body.String())
	}
	var clientSecret struct {
		Value string `json:"value"`
	}
	if errUnmarshal := json.Unmarshal(issueResponse.Body.Bytes(), &clientSecret); errUnmarshal != nil {
		t.Fatalf("decode client secret: %v", errUnmarshal)
	}

	postAuthMu.Lock()
	originalPostAuthHandlers := append([]gin.HandlerFunc(nil), postAuthHandlers...)
	postAuthHandlers = nil
	postAuthCalled := false
	postAuthHandlers = append(postAuthHandlers, func(c *gin.Context) {
		postAuthCalled = true
		if billing.UserIDFromContext(c.Request.Context()) != "user-1" {
			c.AbortWithStatus(http.StatusInternalServerError)
		}
	})
	postAuthMu.Unlock()
	t.Cleanup(func() {
		postAuthMu.Lock()
		postAuthHandlers = originalPostAuthHandlers
		postAuthMu.Unlock()
	})

	router := gin.New()
	router.Use(realtimeAuthMiddleware(accessManager, handler))
	router.POST("/v1/realtime/calls", func(c *gin.Context) {
		ctx := c.Request.Context()
		if got := billing.UserIDFromContext(ctx); got != "user-1" {
			t.Errorf("user id = %q, want user-1", got)
		}
		if got := billing.APIKeyIDFromContext(ctx); got != "key-1" {
			t.Errorf("api key id = %q, want key-1", got)
		}
		if billing.UnbilledFromContext(ctx) {
			t.Error("client secret context retained stale unbilled permission")
		}
		if got := billing.AllowedAuthIDsFromContext(ctx); !reflect.DeepEqual(got, []string{"account-current"}) {
			t.Errorf("allowed auth ids = %v", got)
		}
		if got := billing.AllowedModelsFromContext(ctx); !reflect.DeepEqual(got, []string{"model-1", "model-current"}) {
			t.Errorf("allowed models = %v", got)
		}
		if got, _ := c.Get(codexlive.ClientSecretRequestedModelContextKey); got != "model-1" {
			t.Errorf("requested model = %#v, want model-1", got)
		}
		metadataValue, _ := c.Get("accessMetadata")
		metadata, _ := metadataValue.(map[string]string)
		if metadata["tenant"] != "tenant-current" {
			t.Errorf("access metadata = %#v, want current tenant", metadata)
		}
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	request.Header.Set("Authorization", "Bearer "+clientSecret.Value)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if !postAuthCalled {
		t.Fatal("post-auth handler was not called for Realtime client secret")
	}

	revalidator.revoked = true
	revokedRequest := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	revokedRequest.Header.Set("Authorization", "Bearer "+clientSecret.Value)
	revokedResponse := httptest.NewRecorder()
	router.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked response status = %d, want %d; body=%s", revokedResponse.Code, http.StatusUnauthorized, revokedResponse.Body.String())
	}
}
