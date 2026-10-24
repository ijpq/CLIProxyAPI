package portal

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Private entry HTML must show login before scripts or config requests finish.
func (m *Module) privateUIEntry() gin.HandlerFunc {
	return func(c *gin.Context) {
		if m.siteMode(c) != sitePrivileged {
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		path := c.Param("filepath")
		if path != "/" && path != "/index.html" {
			c.Next()
			return
		}
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		script, err := staticFS.ReadFile("static/app.js")
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		version := fmt.Sprintf("%x", sha256.Sum256(script))[:12]
		html := strings.NewReplacer(
			`<html lang="zh-CN">`, `<html lang="zh-CN" data-portal-site="privileged">`,
			`<title>TrueCore AI · 控制台</title>`, `<title>TrueCore AI · 特权账户登录</title>`,
			`id="page-home" class="landing"`, `id="page-home" class="landing hidden"`,
			`id="page-auth" class="auth-wrap hidden"`, `id="page-auth" class="auth-wrap"`,
			`id="auth-home" class="btn btn-ghost"`, `id="auth-home" class="btn btn-ghost hidden"`,
			`id="auth-title">TrueCore AI`, `id="auth-title">特权账户登录`,
			`id="tab-register" class="tab"`, `id="tab-register" class="tab hidden"`,
			`src="app.js"`, `src="app.js?v=`+version+`"`,
		).Replace(string(data))
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Length", strconv.Itoa(len(html)))
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
		} else {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
		}
		c.Abort()
	}
}

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
	case "portal.css":
		contentType = "text/css; charset=utf-8"
	case "InterVariable.woff2":
		contentType = "font/woff2"
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
		html = strings.ReplaceAll(html, `href="portal.css"`, `href="/api/portal/site/portal.css"`)
		data = []byte(html)
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}
