package portal

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billingvalue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HelperReply executes one bounded text-only support conversation server-side.
type HelperReply func(context.Context, string, []ChatMessage, int) (string, error)

func (m *Module) SetHelper(fn HelperReply) { m.helper = fn }

func (m *Module) handlePublicSite(c *gin.Context) {
	cfg, err := m.store.GetPortalSettings(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "site configuration unavailable"})
		return
	}
	prices, err := m.store.ListPointPrices(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "model pricing unavailable"})
		return
	}
	public := make([]store.PointPrice, 0, len(prices))
	for _, p := range prices {
		if p.Enabled {
			public = append(public, p)
		}
	}
	c.JSON(200, gin.H{"site_name": cfg.SiteName, "public_api_base": cfg.PublicAPIBase, "docs_url": cfg.DocsURL,
		"unit": "POINTS", "cny_points_per_unit": cfg.CNYPointsPerUnit, "usdt_points_per_unit": cfg.USDTPointsPerUnit,
		"helper_enabled": cfg.HelperEnabled && m.helper != nil, "prices": public})
}

func (m *Module) handleAdminGetSettings(c *gin.Context) {
	cfg, err := m.store.GetPortalSettings(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "settings unavailable"})
		return
	}
	c.JSON(200, cfg)
}

func validPublicURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (m *Module) handleAdminSaveSettings(c *gin.Context) {
	var cfg store.PortalSettings
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(400, gin.H{"error": "invalid settings"})
		return
	}
	cfg.SiteName = strings.TrimSpace(cfg.SiteName)
	cfg.PublicAPIBase = strings.TrimRight(strings.TrimSpace(cfg.PublicAPIBase), "/")
	cfg.DocsURL = strings.TrimSpace(cfg.DocsURL)
	cfg.HelperModel = strings.TrimSpace(cfg.HelperModel)
	if cfg.Version <= 0 || cfg.SiteName == "" || utf8.RuneCountInString(cfg.SiteName) > 80 ||
		!validPublicURL(cfg.PublicAPIBase) || !validPublicURL(cfg.DocsURL) ||
		cfg.HelperDailyLimit < 1 || cfg.HelperDailyLimit > 100 || cfg.HelperMaxOutput < 64 || cfg.HelperMaxOutput > 2048 {
		c.JSON(400, gin.H{"error": "invalid site URL, version, name or assistant limits"})
		return
	}
	if cfg.HelperGlobalDailyLimit < 1 || cfg.HelperGlobalDailyLimit > 10000 {
		c.JSON(400, gin.H{"error": "assistant site daily limit must be between 1 and 10000"})
		return
	}
	for _, rate := range []string{cfg.CNYPointsPerUnit, cfg.USDTPointsPerUnit} {
		if rate != "" {
			if _, err := billingvalue.Normalize(rate, true); err != nil {
				c.JSON(400, gin.H{"error": "exchange rates must be positive decimals with at most six fractional digits"})
				return
			}
		}
	}
	if cfg.HelperEnabled {
		prices, err := m.store.ListPointPrices(c.Request.Context())
		if err != nil {
			c.JSON(503, gin.H{"error": "model pricing unavailable"})
			return
		}
		found := false
		for _, p := range prices {
			found = found || (p.Enabled && strings.EqualFold(p.Model, cfg.HelperModel))
		}
		if !found || cfg.PublicAPIBase == "" {
			c.JSON(400, gin.H{"error": "configure a public API base and an enabled priced assistant model first"})
			return
		}
	}
	saved, err := m.store.SavePortalSettings(c.Request.Context(), cfg, userIDFromGin(c))
	if errors.Is(err, store.ErrBillingConflict) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "save settings failed"})
		return
	}
	c.JSON(200, saved)
}

func (m *Module) handleAdminGetPrices(c *gin.Context) {
	prices, err := m.store.ListPointPrices(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "model pricing unavailable"})
		return
	}
	c.JSON(200, gin.H{"unit": "POINTS", "prices": prices})
}

func (m *Module) handleAdminSavePrice(c *gin.Context) {
	var p store.PointPrice
	if err := c.ShouldBindJSON(&p); err != nil || strings.TrimSpace(p.Model) == "" || len(p.Model) > 180 || p.Version < 0 {
		c.JSON(400, gin.H{"error": "invalid model price"})
		return
	}
	var err error
	p, err = store.NormalizePointPrice(p)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	saved, err := m.store.SavePointPrice(c.Request.Context(), p, userIDFromGin(c))
	if errors.Is(err, store.ErrBillingConflict) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "save price failed"})
		return
	}
	c.JSON(200, saved)
}

var credentialPattern = regexp.MustCompile(`(?i)(cpk_[a-z0-9_-]{12,}|sk-[a-z0-9_-]{12,}|bearer\s+[a-z0-9._-]{12,})`)

func (m *Module) handleAssistant(c *gin.Context) {
	cfg, err := m.store.GetPortalSettings(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "assistant configuration unavailable"})
		return
	}
	if !cfg.HelperEnabled || m.helper == nil || cfg.HelperModel == "" {
		c.JSON(503, gin.H{"error": "配置助手尚未启用，请先阅读接入文档"})
		return
	}
	prices, err := m.store.ListPointPrices(c.Request.Context())
	if err != nil {
		c.JSON(503, gin.H{"error": "assistant pricing unavailable"})
		return
	}
	var helperPrice *store.PointPrice
	for _, p := range prices {
		if p.Enabled && strings.EqualFold(p.Model, cfg.HelperModel) {
			helperPrice = &p
			break
		}
	}
	if helperPrice == nil {
		c.JSON(503, gin.H{"error": "assistant model is disabled"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 24<<10)
	var req struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Messages) == 0 || len(req.Messages) > 8 {
		c.JSON(400, gin.H{"error": "send one to eight text messages"})
		return
	}
	total := 0
	for i, msg := range req.Messages {
		total += len(msg.Content)
		if (msg.Role != "user" && msg.Role != "assistant") || strings.TrimSpace(msg.Content) == "" || utf8.RuneCountInString(msg.Content) > 2000 {
			c.JSON(400, gin.H{"error": "only bounded user and assistant text is accepted"})
			return
		}
		req.Messages[i].Content = credentialPattern.ReplaceAllString(msg.Content, "YOUR_API_KEY")
	}
	if total > 12000 || req.Messages[len(req.Messages)-1].Role != "user" {
		c.JSON(400, gin.H{"error": "conversation too long or missing final user message"})
		return
	}
	allowed, err := m.store.ConsumeHelperAllowance(c.Request.Context(), userIDFromGin(c), cfg.HelperDailyLimit, cfg.HelperGlobalDailyLimit)
	if err != nil {
		c.JSON(503, gin.H{"error": "assistant allowance unavailable"})
		return
	}
	if !allowed {
		c.JSON(429, gin.H{"error": "今日配置助手次数已用完，请阅读文档或明天再试"})
		return
	}
	prompt := "你是本服务的客户端配置助手。仅帮助配置 OpenAI 兼容客户端、解释积分充值和排查接入错误。先询问客户端名称和版本。不要索取、生成或回显真实密钥，示例只用 YOUR_API_KEY。不要执行命令、调用工具或声称已修改设备。配置不确定时说明需要核对该客户端官方文档。公开 API Base: " + cfg.PublicAPIBase + "。文档: " + cfg.DocsURL + "。计费单位为积分。"
	messages := append([]ChatMessage{{Role: "system", Content: prompt}}, req.Messages...)
	reply, err := m.helper(billing.WithPointPrice(c.Request.Context(), *helperPrice), cfg.HelperModel, messages, cfg.HelperMaxOutput)
	if err != nil {
		c.JSON(502, gin.H{"error": "配置助手暂时不可用，请稍后重试或查看文档"})
		return
	}
	if len(reply) > 24<<10 {
		c.JSON(502, gin.H{"error": "assistant response exceeded its limit"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"reply": credentialPattern.ReplaceAllString(reply, "YOUR_API_KEY"), "model": cfg.HelperModel, "sponsored": true})
}
