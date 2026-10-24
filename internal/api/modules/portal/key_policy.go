package portal

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

func (m *Module) handleUpdateKeyPolicy(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	var policy store.APIKeyPolicy
	if err := c.ShouldBindJSON(&policy); err != nil || policy.ModelScope == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_scope is required"})
		return
	}
	rec, err := m.store.UpdateAPIKeyPolicy(c.Request.Context(), userIDFromGin(c), c.Param("id"), policy)
	switch {
	case errors.Is(err, store.ErrInvalidKeyPolicy):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model policy"})
	case errors.Is(err, store.ErrAPIKeyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update key policy failed"})
	default:
		c.JSON(http.StatusOK, apiKeyView(rec))
	}
}

// Only public, priced models within the account policy are offered for selection.
func (m *Module) handleKeyModels(c *gin.Context) {
	user, err := m.store.GetUserByID(c.Request.Context(), userIDFromGin(c))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account unavailable"})
		return
	}
	prices, err := m.store.ListPointPrices(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model catalog unavailable"})
		return
	}
	ctx := billing.WithAllowedModels(c.Request.Context(), user.AllowedModels)
	models := make([]string, 0, len(prices))
	availablePrices := make([]store.PointPrice, 0, len(prices))
	for _, price := range prices {
		if price.Enabled && billing.ModelAllowed(ctx, price.Model) {
			models = append(models, price.Model)
			availablePrices = append(availablePrices, price)
		}
	}
	c.JSON(http.StatusOK, gin.H{"models": models, "prices": availablePrices, "unit": "POINTS"})
}
