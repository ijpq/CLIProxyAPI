package portal

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Customer assets use absolute same-origin URLs so nested console pages can
// reload directly. The legacy private UI keeps its existing relative assets.
func (m *Module) handleSiteAsset(c *gin.Context) {
	if mode := m.siteMode(c); mode != siteCustomer && mode != siteLegacy {
		c.Status(http.StatusNotFound)
		return
	}
	name := c.Param("asset")
	contentType := "text/javascript; charset=utf-8"
	switch name {
	case "index.html", "docs.html":
		contentType = "text/html; charset=utf-8"
	case "app.js", "docs.js":
	default:
		c.Status(http.StatusNotFound)
		return
	}
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if contentType == "text/html; charset=utf-8" {
		html := strings.ReplaceAll(string(data), `src="app.js"`, `src="/assets/app.js"`)
		html = strings.ReplaceAll(html, `src="docs.js"`, `src="/assets/docs.js"`)
		data = []byte(html)
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}
