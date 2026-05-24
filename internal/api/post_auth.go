package api

import (
	"sync"

	"github.com/gin-gonic/gin"
)

// postAuthHandlers runs after AuthMiddleware has successfully resolved the
// caller identity and attached the billing user/key ids to the request
// context. Handlers can abort the request (c.Abort + status) to deny access
// (e.g. on insufficient balance or rate limit exceeded).
var (
	postAuthMu       sync.RWMutex
	postAuthHandlers []gin.HandlerFunc
)

// RegisterPostAuthHandler appends a handler to the global chain executed by
// AuthMiddleware on every authenticated request. Registration must happen
// before the server starts handling traffic.
func RegisterPostAuthHandler(fn gin.HandlerFunc) {
	if fn == nil {
		return
	}
	postAuthMu.Lock()
	postAuthHandlers = append(postAuthHandlers, fn)
	postAuthMu.Unlock()
}

// runPostAuthHandlers invokes the registered handlers in order. It returns
// true when one of them aborted the request so the caller can stop processing.
func runPostAuthHandlers(c *gin.Context) bool {
	postAuthMu.RLock()
	handlers := make([]gin.HandlerFunc, len(postAuthHandlers))
	copy(handlers, postAuthHandlers)
	postAuthMu.RUnlock()
	for _, fn := range handlers {
		fn(c)
		if c.IsAborted() {
			return true
		}
	}
	return false
}
