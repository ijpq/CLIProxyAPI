package api

import (
	"bufio"
	"io"
	"net"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	codexlive "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/safemode"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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
	// Cross-origin management clients must read the config version token to
	// send it back as If-Match on writes.
	"ETag",
	"X-Request-Id",
	"OpenAI-Request-Id",
}

var corsExposedResponseHeadersJoined = strings.Join(corsExposedResponseHeaders, ", ")

// corsAllowedRequestHeaders lists the request headers browser clients may
// send to the API. The Fetch standard treats Authorization as a CORS
// non-wildcard request-header name, so it must be named explicitly: a
// wildcard does not cover it when a browser enforces that rule.
var corsAllowedRequestHeaders = []string{
	"Authorization",
	"Content-Type",
	"X-Api-Key",
	"X-Goog-Api-Key",
	"Anthropic-Version",
	"Anthropic-Beta",
}

const (
	exampleAPIKeyManagementPath = "/management.html"
	exampleAPIKeyManagementURL  = "/management.html?safe-mode=configure"
)

func (s *Server) maxRequestBodyBytes() int64 {
	if s == nil {
		return 0
	}
	cfg := s.getConfig()
	if cfg == nil || cfg.MaxRequestBodyBytes <= 0 {
		return 0
	}
	return cfg.MaxRequestBodyBytes
}

type requestBodyTracker struct {
	io.ReadCloser
	contentLength int64
	bytesRead     int64
	sawEOF        bool
}

func (t *requestBodyTracker) Read(p []byte) (int, error) {
	if t == nil || t.ReadCloser == nil {
		return 0, io.EOF
	}
	n, errRead := t.ReadCloser.Read(p)
	if n > 0 {
		t.bytesRead += int64(n)
	}
	if errors.Is(errRead, io.EOF) {
		t.sawEOF = true
	}
	return n, errRead
}

func (t *requestBodyTracker) isComplete() bool {
	if t == nil {
		return true
	}
	return t.sawEOF || (t.contentLength >= 0 && t.bytesRead >= t.contentLength)
}

type unconsumedBodyHTTPResponseWriter struct {
	http.ResponseWriter
	tracker     *requestBodyTracker
	status      int
	wroteHeader bool
	hijacked    bool
	closed      bool
}

func (w *unconsumedBodyHTTPResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *unconsumedBodyHTTPResponseWriter) WriteHeader(code int) {
	w.status = code
	w.closeIfBodyUnconsumed(code)
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *unconsumedBodyHTTPResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.closeIfBodyUnconsumed(w.status)
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *unconsumedBodyHTTPResponseWriter) WriteString(s string) (int, error) {
	if !w.wroteHeader {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.closeIfBodyUnconsumed(w.status)
		w.wroteHeader = true
	}
	return io.WriteString(w.ResponseWriter, s)
}

func (w *unconsumedBodyHTTPResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, errHijack := hijacker.Hijack()
	if errHijack == nil {
		w.hijacked = true
	}
	return conn, rw, errHijack
}

func (w *unconsumedBodyHTTPResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *unconsumedBodyHTTPResponseWriter) CloseNotify() <-chan bool {
	if notifier, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return nil
}

func (w *unconsumedBodyHTTPResponseWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (w *unconsumedBodyHTTPResponseWriter) closeIfBodyUnconsumed(status int) {
	if w == nil || w.closed || w.hijacked || status < http.StatusOK {
		return
	}
	if w.tracker != nil && w.tracker.isComplete() {
		return
	}
	w.closed = true
	if !w.wroteHeader {
		w.ResponseWriter.Header().Set("Connection", "close")
	}
	_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

func wrapUnconsumedRequestBodyHandler(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || r.Body == nil || r.Body == http.NoBody || (r.ContentLength == 0 && len(r.TransferEncoding) == 0) {
			next.ServeHTTP(w, r)
			return
		}
		tracker, ok := r.Body.(*requestBodyTracker)
		if !ok {
			tracker = &requestBodyTracker{
				ReadCloser:    r.Body,
				contentLength: r.ContentLength,
			}
			r.Body = tracker
		}
		wrappedWriter := &unconsumedBodyHTTPResponseWriter{
			ResponseWriter: w,
			tracker:        tracker,
		}
		defer func() {
			status := wrappedWriter.status
			if status == 0 {
				status = http.StatusOK
			}
			wrappedWriter.closeIfBodyUnconsumed(status)
		}()
		next.ServeHTTP(wrappedWriter, r)
	})
}

type unconsumedBodyCloseWriter struct {
	gin.ResponseWriter
	tracker  *requestBodyTracker
	hijacked bool
	closed   bool
}

func (w *unconsumedBodyCloseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *unconsumedBodyCloseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, errHijack := w.ResponseWriter.Hijack()
	if errHijack == nil {
		w.hijacked = true
	}
	return conn, rw, errHijack
}

func (w *unconsumedBodyCloseWriter) WriteHeader(code int) {
	w.closeIfBodyUnconsumed(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *unconsumedBodyCloseWriter) WriteHeaderNow() {
	w.closeIfBodyUnconsumed(w.Status())
	w.ResponseWriter.WriteHeaderNow()
}

func (w *unconsumedBodyCloseWriter) Write(data []byte) (int, error) {
	w.closeIfBodyUnconsumed(w.Status())
	return w.ResponseWriter.Write(data)
}

func (w *unconsumedBodyCloseWriter) WriteString(data string) (int, error) {
	w.closeIfBodyUnconsumed(w.Status())
	return w.ResponseWriter.WriteString(data)
}

func (w *unconsumedBodyCloseWriter) Flush() {
	_ = w.FlushError()
}

func (w *unconsumedBodyCloseWriter) FlushError() error {
	w.closeIfBodyUnconsumed(w.Status())
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *unconsumedBodyCloseWriter) closeIfBodyUnconsumed(status int) {
	if w == nil || w.closed || w.hijacked || status < http.StatusOK {
		return
	}
	if w.tracker != nil && w.tracker.isComplete() {
		return
	}
	w.closed = true
	if !w.ResponseWriter.Written() {
		w.ResponseWriter.Header().Set("Connection", "close")
	}
	_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Unix(1, 0))
}

func (s *Server) maxRequestBodyBytesMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			c.Next()
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody && (c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0) {
			tracker, ok := c.Request.Body.(*requestBodyTracker)
			if !ok {
				tracker = &requestBodyTracker{
					ReadCloser:    c.Request.Body,
					contentLength: c.Request.ContentLength,
				}
				c.Request.Body = tracker
			}
			closeWriter := &unconsumedBodyCloseWriter{
				ResponseWriter: c.Writer,
				tracker:        tracker,
			}
			c.Writer = closeWriter
			defer func() {
				closeWriter.closeIfBodyUnconsumed(c.Writer.Status())
			}()
		}
		maxBytes := s.maxRequestBodyBytes()
		if maxBytes <= 0 {
			c.Next()
			return
		}
		c.Set(handlers.MaxRequestBodyBytesContextKey, maxBytes)
		if c.Request.ContentLength > maxBytes {
			closeUnconsumedRequestOnAbort(c)
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "http: request body too large",
			})
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func (s *Server) homeHeartbeatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if s == nil || cfg == nil || !cfg.Home.Enabled {
			c.Next()
			return
		}
		if c != nil && c.Request != nil {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || strings.HasPrefix(path, "/v8/management/") || path == "/v8/management" || strings.HasPrefix(path, "/v0/resource/plugins/") || path == "/management.html" || path == "/request-logs.html" {
				c.Next()
				return
			}
		}
		client := home.Current()
		if client == nil || !client.HeartbeatOK() {
			closeUnconsumedRequestOnAbort(c)
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

		closeUnconsumedRequestOnAbort(c)
		c.Header("X-CPA-SAFE-MODE", "example-api-key")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "unsafe_example_api_key",
			"message": "Proxy API endpoints are disabled because api-keys contains template values. Open /management.html?safe-mode=configure, update api-keys in Management, then retry.",
		})
	}
}

func (s *Server) serveExampleAPIKeyWarningPage(c *gin.Context) {
	cfg := s.getConfig()
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
	case path == "/muse-code" || strings.HasPrefix(path, "/muse-code/"):
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
		c.Header("Access-Control-Allow-Headers", corsAllowHeadersValue(c.Request))
		c.Header("Access-Control-Expose-Headers", corsExposedResponseHeadersJoined)

		if c.Request.Method == "OPTIONS" {
			closeUnconsumedRequestOnAbort(c)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// corsAllowHeadersValue builds the Access-Control-Allow-Headers value for a
// request. It always names the known API request headers explicitly and, for
// preflight requests, also echoes the headers the client asked about, so no
// allowed header depends on the wildcard that strict non-wildcard rules
// reject for Authorization. Echoing requested headers preserves the previous
// wildcard behavior for arbitrary custom client headers without widening
// the allowed origins or enabling credentials.
func corsAllowHeadersValue(req *http.Request) string {
	allowed := make([]string, 0, len(corsAllowedRequestHeaders))
	seen := make(map[string]struct{}, len(corsAllowedRequestHeaders))
	appendHeader := func(name string) {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		allowed = append(allowed, name)
	}
	for _, name := range corsAllowedRequestHeaders {
		appendHeader(name)
	}
	if req != nil && req.Method == http.MethodOptions {
		for _, name := range strings.Split(req.Header.Get("Access-Control-Request-Headers"), ",") {
			appendHeader(name)
		}
	}
	return strings.Join(allowed, ", ")
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

func closeUnconsumedRequestOnAbort(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 {
		c.Header("Connection", "close")
		_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Unix(1, 0))
	}
}

func accessAuthMiddleware(manager *sdkaccess.Manager, realtimeError bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			middleware.CaptureAuthenticatedRequestBody(c)
			if c.IsAborted() {
				return
			}
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
				reqCtx := coreusage.WithAPIKey(c.Request.Context(), result.Principal)
				reqCtx = coreusage.WithAccessProvider(reqCtx, result.Provider)
				reqCtx = coreusage.WithIsNativeKey(reqCtx, coreusage.IsNativeAccessProvider(result.Provider))
				if userID := result.Metadata[billing.MetadataKeyUserID]; userID != "" {
					c.Set(billing.MetadataKeyUserID, userID)
					reqCtx = billing.WithUserID(reqCtx, userID)
				}
				if keyID := result.Metadata[billing.MetadataKeyAPIKeyID]; keyID != "" {
					c.Set(billing.MetadataKeyAPIKeyID, keyID)
					reqCtx = billing.WithAPIKeyID(reqCtx, keyID)
				}
				if result.Metadata[billing.MetadataKeyUnbilled] == "1" {
					reqCtx = billing.WithUnbilled(reqCtx)
				}
				if allowed := result.Metadata[billing.MetadataKeyAllowedAuthIDs]; allowed != "" {
					reqCtx = billing.WithAllowedAuthIDs(reqCtx, billing.SplitCSV(allowed))
				}
				if allowed := result.Metadata[billing.MetadataKeyAllowedModels]; allowed != "" {
					reqCtx = billing.WithAllowedModels(reqCtx, billing.SplitCSV(allowed))
				}
				c.Request = c.Request.WithContext(reqCtx)
			}
			middleware.CaptureAuthenticatedRequestBody(c)
			if c.IsAborted() {
				return
			}
			if runPostAuthHandlers(c) {
				return
			}
			c.Next()
			return
		}

		closeUnconsumedRequestOnAbort(c)
		statusCode, message := authenticationErrorStatus(err)
		if statusCode >= http.StatusInternalServerError {
			log.Errorf("authentication middleware error: %v", err)
		}
		if realtimeError {
			errorType := "authentication_error"
			code := "invalid_api_key"
			switch statusCode {
			case http.StatusRequestEntityTooLarge:
				errorType = "invalid_request_error"
				code = "request_body_too_large"
			case clienterror.StatusClientClosedRequest:
				errorType = "request_error"
				code = "request_canceled"
			case http.StatusGatewayTimeout:
				errorType = "server_error"
				code = "authentication_timeout"
			default:
				if statusCode >= http.StatusInternalServerError {
					errorType = "server_error"
					code = "authentication_service_error"
				}
			}
			c.AbortWithStatusJSON(statusCode, gin.H{"error": gin.H{
				"message": message,
				"type":    errorType,
				"param":   nil,
				"code":    code,
			}})
			return
		}
		c.AbortWithStatusJSON(statusCode, gin.H{"error": message})
	}
}

func authenticationErrorStatus(authErr *sdkaccess.AuthError) (int, string) {
	if errors.Is(authErr, context.Canceled) {
		return clienterror.StatusClientClosedRequest, "Request canceled"
	}
	if errors.Is(authErr, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "Authentication request timed out"
	}
	if authErr == nil {
		return http.StatusInternalServerError, "Authentication service error"
	}
	return authErr.HTTPStatusCode(), authErr.Message
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
			closeUnconsumedRequestOnAbort(c)
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
		ctx := coreusage.WithAPIKey(c.Request.Context(), principal)
		ctx = coreusage.WithAccessProvider(ctx, provider)
		ctx = coreusage.WithIsNativeKey(ctx, coreusage.IsNativeAccessProvider(provider))
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
		middleware.CaptureAuthenticatedRequestBody(c)
		if c.IsAborted() || runPostAuthHandlers(c) {
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
