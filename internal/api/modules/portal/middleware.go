package portal

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
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
		c.Set(ginCtxUserID, claims.UserID)
		c.Set(ginCtxIsAdmin, claims.IsAdmin)
		c.Request = c.Request.WithContext(billing.WithUserID(c.Request.Context(), claims.UserID))
		c.Next()
	}
}

func userIDFromGin(c *gin.Context) string {
	v, _ := c.Get(ginCtxUserID)
	id, _ := v.(string)
	return id
}

// adminOnly rejects requests whose token does not carry the admin flag.
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
