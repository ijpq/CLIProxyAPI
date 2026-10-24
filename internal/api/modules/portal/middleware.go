package portal

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

const (
	ginCtxUserID  = "portal_user_id"
	ginCtxIsAdmin = "portal_is_admin"
)

// AuthMiddleware validates the bearer token on inbound requests and stores
// the resolved user id on the gin context for downstream handlers.
func (m *Module) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearer(c.GetHeader("Authorization"))
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		claims, err := m.tokens.Parse(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		user, err := m.store.GetUserByID(c.Request.Context(), claims.UserID)
		if errors.Is(err, store.ErrUserNotFound) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "account lookup failed"})
			return
		}
		if user.Status != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account suspended"})
			return
		}
		// A signed claim identifies the session; current database state decides access.
		c.Set(ginCtxUserID, user.ID)
		c.Set(ginCtxIsAdmin, user.IsAdmin)
		c.Request = c.Request.WithContext(billing.WithUserID(c.Request.Context(), claims.UserID))
		c.Next()
	}
}

func userIDFromGin(c *gin.Context) string {
	v, _ := c.Get(ginCtxUserID)
	id, _ := v.(string)
	return id
}

func isAdminFromGin(c *gin.Context) bool {
	v, _ := c.Get(ginCtxIsAdmin)
	admin, _ := v.(bool)
	return admin
}

// adminOnly rejects requests whose current user is not an administrator.
// Must be installed after AuthMiddleware.
func (m *Module) adminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		v, _ := c.Get(ginCtxIsAdmin)
		if isAdmin, _ := v.(bool); !isAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin only"})
			return
		}
		c.Next()
	}
}

func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
