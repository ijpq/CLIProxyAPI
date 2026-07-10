package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

type emailRequest struct {
	Email string `json:"email"`
}

type codeVerifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// handlePortalConfig reports which auth methods the UI should offer. Public.
func (m *Module) handlePortalConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"email_login": m.emailLoginEnabled()})
}

// handleRequestLoginCode issues and emails a one-time login code. Public.
func (m *Module) handleRequestLoginCode(c *gin.Context) {
	if !m.emailLoginEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email login not configured"})
		return
	}
	var req emailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid email"})
		return
	}

	code, err := m.loginCodes.Generate(email)
	if err != nil {
		if errors.Is(err, billing.ErrCodeCooldown) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "验证码请求过于频繁，请稍后再试"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate code failed"})
		return
	}

	sendCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if err := m.emailSender.Send(sendCtx, email, "登录验证码", loginCodeEmailHTML(code)); err != nil {
		log.WithError(err).Errorf("billing: send login code to %s failed", email)
		c.JSON(http.StatusBadGateway, gin.H{"error": "验证码邮件发送失败，请稍后再试"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleVerifyLoginCode verifies a login code and issues a session token,
// auto-registering the account on first use. Public.
func (m *Module) handleVerifyLoginCode(c *gin.Context) {
	if !m.emailLoginEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email login not configured"})
		return
	}
	var req codeVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !m.loginCodes.Verify(email, req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "验证码错误或已过期"})
		return
	}

	user, err := m.getOrCreateUserByEmail(c.Request.Context(), email)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "login failed"})
		return
	}
	token, err := m.tokens.Issue(user.ID, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "user": userView(user)})
}

// getOrCreateUserByEmail returns the user with the given email, creating a
// passwordless account (random password hash) on first email-code login.
func (m *Module) getOrCreateUserByEmail(ctx context.Context, email string) (store.User, error) {
	user, err := m.store.GetUserByEmail(ctx, email)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, store.ErrUserNotFound) {
		return store.User{}, err
	}
	// Auto-register: the password is random (the user logs in via email code;
	// they can set a real password later on the settings page).
	var buf [24]byte
	if _, rerr := rand.Read(buf[:]); rerr != nil {
		return store.User{}, rerr
	}
	hash, herr := billing.HashPassword(hex.EncodeToString(buf[:]))
	if herr != nil {
		return store.User{}, herr
	}
	user, err = m.store.CreateUser(ctx, email, hash, "")
	if errors.Is(err, store.ErrEmailTaken) {
		// Lost a race with a concurrent create; load the existing row.
		return m.store.GetUserByEmail(ctx, email)
	}
	if err != nil {
		return store.User{}, err
	}
	m.notify(ctx, fmt.Sprintf("🆕 新用户（邮箱验证码）: %s", email))
	return user, nil
}

func loginCodeEmailHTML(code string) string {
	return fmt.Sprintf(`<div style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;max-width:440px;margin:0 auto;padding:24px;color:#1e293b">
  <h2 style="margin:0 0 8px">登录验证码</h2>
  <p style="color:#64748b;margin:0 0 20px">用下面的验证码登录 API Console，10 分钟内有效。</p>
  <div style="font-size:34px;font-weight:800;letter-spacing:10px;background:#f1f5f9;border-radius:12px;padding:18px;text-align:center">%s</div>
  <p style="color:#94a3b8;font-size:13px;margin-top:20px">如果不是你本人操作，忽略此邮件即可。</p>
</div>`, code)
}
