package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

var corsExposedResponseHeaders = []string{
	logging.CPATraceIDHeader,
	"X-CPA-VERSION",
	"X-CPA-COMMIT",
	"X-CPA-BUILD-DATE",
	"X-CPA-SUPPORT-PLUGIN",
	"X-CPA-HOME-VERSION",
	"X-CPA-HOME-BUILD-DATE",
	"X-SERVER-VERSION",
	"X-SERVER-BUILD-DATE",
	"Location",
	"Retry-After",
	"X-Request-Id",
	"OpenAI-Request-Id",
}

var corsExposedResponseHeadersJoined = strings.Join(corsExposedResponseHeaders, ", ")

const (
	exampleAPIKeyManagementPath = "/management.html"
	exampleAPIKeyManagementURL  = "/management.html?safe-mode=configure"
)

func (s *Server) homeHeartbeatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.cfg == nil || !s.cfg.Home.Enabled {
			c.Next()
			return
		}
		if c != nil && c.Request != nil {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || strings.HasPrefix(path, "/v0/resource/plugins/") || path == "/management.html" || path == "/request-logs.html" {
				c.Next()
				return
			}
		}
		client := home.Current()
		if client == nil || !client.HeartbeatOK() {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		c.Next()
	}
}

func (s *Server) exampleAPIKeySafeModeRequired(cfg *config.Config) bool {
	return s != nil && s.exampleAPIKeySafeModeEnabled && cfg != nil && safemode.HasExampleAPIKeys(cfg.APIKeys)
}

func (s *Server) exampleAPIKeySafeModeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || !s.exampleAPIKeySafeModeActive.Load() || c == nil || c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if path == exampleAPIKeyManagementPath && c.Query("safe-mode") == "configure" {
			c.Next()
			return
		}
		if (path == "/" || path == exampleAPIKeyManagementPath) && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
			s.serveExampleAPIKeyWarningPage(c)
			return
		}
		if !isExampleAPIKeySafeModeProxyPath(path) {
			c.Next()
			return
		}

		c.Header("X-CPA-SAFE-MODE", "example-api-key")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "unsafe_example_api_key",
			"message": "Proxy API endpoints are disabled because api-keys contains template values. Open /management.html?safe-mode=configure, update api-keys in Management, then retry.",
		})
	}
}

func (s *Server) serveExampleAPIKeyWarningPage(c *gin.Context) {
	cfg := s.cfg
	var keys []string
	if cfg != nil {
		keys = safemode.ExampleAPIKeys(cfg.APIKeys)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		c.Abort()
		return
	}
	c.String(http.StatusOK, safemode.ExampleAPIKeyWarningPageHTML(keys, exampleAPIKeyManagementURL))
	c.Abort()
}

func isExampleAPIKeySafeModeProxyPath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/v1beta" || strings.HasPrefix(path, "/v1beta/"):
		return true
	case path == "/openai/v1" || strings.HasPrefix(path, "/openai/v1/"):
		return true
	case path == "/backend-api/codex" || strings.HasPrefix(path, "/backend-api/codex/"):
		return true
	default:
		return false
	}
}

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Expose-Headers", corsExposedResponseHeadersJoined)

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using the configured authentication providers. When no providers are available,
// it allows all requests (legacy behaviour).
func AuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	return accessAuthMiddleware(manager, false)
}

func realtimeStandardAuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	return accessAuthMiddleware(manager, true)
}

func accessAuthMiddleware(manager *sdkaccess.Manager, realtimeError bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		result, err := manager.Authenticate(c.Request.Context(), c.Request)
		if err == nil {
			if result != nil {
				c.Set("userApiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
				ctx := c.Request.Context()
				if userID := result.Metadata[billing.MetadataKeyUserID]; userID != "" {
					c.Set(billing.MetadataKeyUserID, userID)
					ctx = billing.WithUserID(ctx, userID)
				}
				if keyID := result.Metadata[billing.MetadataKeyAPIKeyID]; keyID != "" {
					c.Set(billing.MetadataKeyAPIKeyID, keyID)
					ctx = billing.WithAPIKeyID(ctx, keyID)
				}
				if result.Metadata[billing.MetadataKeyUnbilled] == "1" {
					ctx = billing.WithUnbilled(ctx)
				}
				if allowed := result.Metadata[billing.MetadataKeyAllowedAuthIDs]; allowed != "" {
					ctx = billing.WithAllowedAuthIDs(ctx, billing.SplitCSV(allowed))
				}
				if allowed := result.Metadata[billing.MetadataKeyAllowedModels]; allowed != "" {
					ctx = billing.WithAllowedModels(ctx, billing.SplitCSV(allowed))
				}
				c.Request = c.Request.WithContext(ctx)
			}
			if runPostAuthHandlers(c) {
				return
			}
			c.Next()
			return
		}

		statusCode := err.HTTPStatusCode()
		if statusCode >= http.StatusInternalServerError {
			log.Errorf("authentication middleware error: %v", err)
		}
		if realtimeError {
			errorType := "authentication_error"
			code := "invalid_api_key"
			if statusCode >= http.StatusInternalServerError {
				errorType = "server_error"
				code = "authentication_service_error"
			}
			c.AbortWithStatusJSON(statusCode, gin.H{"error": gin.H{
				"message": err.Message,
				"type":    errorType,
				"param":   nil,
				"code":    code,
			}})
			return
		}
		c.AbortWithStatusJSON(statusCode, gin.H{"error": err.Message})
	}
}

func realtimeAuthMiddleware(manager *sdkaccess.Manager, handler *codexlive.Handler) gin.HandlerFunc {
	fallback := realtimeStandardAuthMiddleware(manager)
	return func(c *gin.Context) {
		authorization, matched, errAuthenticate := handler.AuthenticateClientSecret(c.Request)
		if !matched {
			fallback(c)
			return
		}
		if errAuthenticate != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
				"message": errAuthenticate.Error(),
				"type":    "invalid_request_error",
				"param":   nil,
				"code":    "invalid_realtime_client_secret",
			}})
			return
		}
		authorization, errRevalidate := revalidateRealtimeClientSecret(c.Request.Context(), manager, authorization)
		if errRevalidate != nil {
			status := errRevalidate.HTTPStatusCode()
			if status == http.StatusUnauthorized {
				handler.CloseRevokedClientSecretSession(c, authorization)
			}
			errorType := "authentication_error"
			code := "invalid_realtime_client_secret"
			if status >= http.StatusInternalServerError {
				log.Errorf("Realtime client secret revalidation error: %v", errRevalidate)
				errorType = "server_error"
				code = "authentication_service_error"
			}
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
				"message": errRevalidate.Message,
				"type":    errorType,
				"param":   nil,
				"code":    code,
			}})
			return
		}
		principal := authorization.IssuerPrincipal
		if principal == "" {
			principal = authorization.Principal
		}
		provider := authorization.IssuerProvider
		if provider == "" {
			provider = "realtime-client-secret"
		}
		c.Set("userApiKey", principal)
		c.Set("accessProvider", provider)
		if len(authorization.IssuerMetadata) > 0 {
			c.Set("accessMetadata", authorization.IssuerMetadata)
		}
		c.Set(codexlive.ClientSecretSessionContextKey, authorization.Session)
		c.Set(codexlive.ClientSecretPrincipalContextKey, authorization.Principal)
		c.Set(codexlive.ClientSecretRequestedModelContextKey, authorization.RequestedModel)
		ctx := c.Request.Context()
		if authorization.IssuerUserID != "" {
			c.Set(billing.MetadataKeyUserID, authorization.IssuerUserID)
			ctx = billing.WithUserID(ctx, authorization.IssuerUserID)
		}
		if authorization.IssuerAPIKeyID != "" {
			c.Set(billing.MetadataKeyAPIKeyID, authorization.IssuerAPIKeyID)
			ctx = billing.WithAPIKeyID(ctx, authorization.IssuerAPIKeyID)
		}
		if authorization.IssuerUnbilled {
			ctx = billing.WithUnbilled(ctx)
		}
		ctx = billing.WithAllowedAuthIDs(ctx, authorization.AllowedAuthIDs)
		ctx = billing.WithAllowedModels(ctx, authorization.AllowedModels)
		c.Request = c.Request.WithContext(ctx)
		if runPostAuthHandlers(c) {
			return
		}
		c.Next()
	}
}

func revalidateRealtimeClientSecret(ctx context.Context, manager *sdkaccess.Manager, authorization codexlive.ClientSecretAuthorization) (codexlive.ClientSecretAuthorization, *sdkaccess.AuthError) {
	issuerProvider := strings.TrimSpace(authorization.IssuerProvider)
	revalidationRequired := authorization.IssuerAPIKeyID != "" || issuerProvider == sdkaccess.DefaultAccessProviderName || issuerProvider == sdkaccess.DefaultDBAccessProviderName
	if manager == nil {
		if revalidationRequired {
			return authorization, sdkaccess.NewInternalAuthError("Realtime client secret issuer cannot be revalidated", nil)
		}
		return authorization, nil
	}
	metadata := make(map[string]string, len(authorization.IssuerMetadata)+1)
	for key, value := range authorization.IssuerMetadata {
		metadata[key] = value
	}
	if authorization.IssuerAPIKeyID != "" {
		metadata[billing.MetadataKeyAPIKeyID] = authorization.IssuerAPIKeyID
	}
	result, handled, authErr := manager.Revalidate(ctx, &sdkaccess.Result{
		Provider:  authorization.IssuerProvider,
		Principal: authorization.IssuerPrincipal,
		Metadata:  metadata,
	})
	if authErr != nil {
		return authorization, authErr
	}
	if !handled {
		if revalidationRequired {
			return authorization, sdkaccess.NewInternalAuthError("Realtime client secret issuer cannot be revalidated", nil)
		}
		return authorization, nil
	}
	authorization.IssuerPrincipal = result.Principal
	authorization.IssuerProvider = result.Provider
	authorization.IssuerMetadata = make(map[string]string, len(result.Metadata))
	for key, value := range result.Metadata {
		authorization.IssuerMetadata[key] = value
	}
	authorization.IssuerUserID = result.Metadata[billing.MetadataKeyUserID]
	authorization.IssuerAPIKeyID = result.Metadata[billing.MetadataKeyAPIKeyID]
	authorization.IssuerUnbilled = result.Metadata[billing.MetadataKeyUnbilled] == "1"
	authorization.AllowedAuthIDs = billing.SplitCSV(result.Metadata[billing.MetadataKeyAllowedAuthIDs])
	authorization.AllowedModels = billing.SplitCSV(result.Metadata[billing.MetadataKeyAllowedModels])
	return authorization, nil
}
