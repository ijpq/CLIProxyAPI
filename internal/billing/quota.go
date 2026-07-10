package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"github.com/tidwall/gjson"
)

// AntigravityCredits mirrors the credits state the Antigravity executor already
// tracks per auth (sdk/cliproxy/auth.AntigravityCreditsHint). It is resolved by
// the wiring layer so this package stays free of the core auth manager.
type AntigravityCredits struct {
	Known           bool
	Available       bool
	CreditAmount    float64
	MinCreditAmount float64
	UpdatedAt       time.Time
}

// UpstreamAuth is the minimal per-account information the quota fetcher needs to
// perform a live upstream quota lookup. Token is the bearer access token used in
// the "$TOKEN$" position; it is kept fresh by the provider executors.
type UpstreamAuth struct {
	ID       string
	Provider string
	Token    string
	ProxyURL string
	// Antigravity credits are tracked in-process by the executor, so no live
	// token call is required; the resolver fills this for antigravity accounts.
	AntigravityCredits *AntigravityCredits
}

// AuthResolver resolves an upstream account (by its billing account id) to the
// data needed for a live quota lookup. It is implemented in cmd/server over the
// core auth manager. A nil resolver disables quota lookups.
type AuthResolver func(id string) (UpstreamAuth, bool)

var quotaAuthResolver AuthResolver

// SetAuthResolver registers the upstream-account resolver used by quota lookups.
func SetAuthResolver(r AuthResolver) { quotaAuthResolver = r }

// QuotaWindow is a normalized usage window shown to end users. UsedPercent and
// RemainingPercent are 0..100 (nil when the provider did not report a number).
type QuotaWindow struct {
	ID               string     `json:"id"`
	Label            string     `json:"label"`
	UsedPercent      *float64   `json:"used_percent"`
	RemainingPercent *float64   `json:"remaining_percent"`
	ResetsAt         *time.Time `json:"resets_at"`
}

// QuotaDetail is a labeled scalar shown alongside the window bars (plan type,
// subscription expiry, extra-usage credits, reset credits, etc.).
type QuotaDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// AccountQuota is the per-account quota view returned to end users. It never
// exposes the raw credential id/email — Label is the customer-safe name.
type AccountQuota struct {
	AuthID       string        `json:"auth_id"`
	Provider     string        `json:"provider"`
	Label        string        `json:"label"`
	PlanType     string        `json:"plan_type,omitempty"`
	Details      []QuotaDetail `json:"details,omitempty"`
	Windows      []QuotaWindow `json:"windows"`
	ResetCredits int           `json:"reset_credits,omitempty"`
	CanReset     bool          `json:"can_reset,omitempty"`
	Note         string        `json:"note,omitempty"`
	Error        string        `json:"error,omitempty"`
}

func (q *AccountQuota) addDetail(label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	q.Details = append(q.Details, QuotaDetail{Label: label, Value: value})
}

// Upstream quota endpoints (mirrors the CLI Proxy API Management Center). The
// providers return their own live usage state; we proxy the call with the
// account's token and normalize the payload.
const (
	claudeUsageURL       = "https://api.anthropic.com/api/oauth/usage"
	claudeProfileURL     = "https://api.anthropic.com/api/oauth/profile"
	codexUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexResetConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	claudeUsageUserAgent = "cliproxyapi"
	codexUsageUserAgent  = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"
)

// FetchAccountQuota performs a live quota lookup for a single upstream account
// and returns a normalized, customer-safe view. It never returns an error: any
// failure is surfaced in AccountQuota.Error so one bad account does not break
// the whole page.
func FetchAccountQuota(ctx context.Context, id, provider, label string) AccountQuota {
	out := AccountQuota{AuthID: id, Provider: provider, Label: label}
	resolver := quotaAuthResolver
	if resolver == nil {
		out.Error = "quota lookup unavailable"
		return out
	}
	ua, ok := resolver(id)
	if !ok {
		out.Error = "account not found"
		return out
	}
	if out.Provider == "" {
		out.Provider = ua.Provider
	}

	switch strings.ToLower(strings.TrimSpace(ua.Provider)) {
	case "claude":
		if err := fetchClaudeQuota(ctx, ua, &out); err != nil {
			out.Error = err.Error()
		}
	case "codex":
		if err := fetchCodexQuota(ctx, ua, &out); err != nil {
			out.Error = err.Error()
		}
	case "antigravity":
		applyAntigravityCredits(&out, ua.AntigravityCredits)
	default:
		out.Note = "该提供方暂不支持余额查询"
	}
	return out
}

// FetchAccountQuotas fetches quota for many accounts concurrently, preserving
// the input order. The passed context should carry a deadline so a single slow
// upstream cannot stall the whole request.
func FetchAccountQuotas(ctx context.Context, accounts []AccountQuota) []AccountQuota {
	out := make([]AccountQuota, len(accounts))
	type result struct {
		i int
		q AccountQuota
	}
	ch := make(chan result, len(accounts))
	for i, a := range accounts {
		go func(i int, a AccountQuota) {
			ch <- result{i: i, q: FetchAccountQuota(ctx, a.AuthID, a.Provider, a.Label)}
		}(i, a)
	}
	for range accounts {
		r := <-ch
		out[r.i] = r.q
	}
	return out
}

// AccountHandle returns a stable, non-reversible handle for an upstream account
// id. The portal references accounts by this handle so the raw credential id
// (which may embed the operator's email) never reaches the client.
func AccountHandle(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:])[:16]
}

// ConsumeCodexResetCredit redeems one Codex rate-limit reset credit for the
// account, resetting its 5h limit early. It mirrors the Management Center's
// "reset quota" action. Errors when the account is not Codex or the upstream
// rejects the redemption.
func ConsumeCodexResetCredit(ctx context.Context, id string) error {
	resolver := quotaAuthResolver
	if resolver == nil {
		return fmt.Errorf("quota lookup unavailable")
	}
	ua, ok := resolver(id)
	if !ok {
		return fmt.Errorf("account not found")
	}
	if !strings.EqualFold(strings.TrimSpace(ua.Provider), "codex") {
		return fmt.Errorf("仅 Codex 账号支持重置额度")
	}
	body := []byte(fmt.Sprintf(`{"redeem_request_id":%q}`, randomRequestID()))
	_, status, err := quotaDo(ctx, ua, http.MethodPost, codexResetConsumeURL, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   codexUsageUserAgent,
	}, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return statusError(status)
	}
	return nil
}

// randomRequestID returns a random UUID-v4 string for idempotent redemption.
func randomRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func fetchClaudeQuota(ctx context.Context, ua UpstreamAuth, out *AccountQuota) error {
	headers := map[string]string{
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
		"User-Agent":     claudeUsageUserAgent,
	}
	body, status, err := quotaGet(ctx, ua, claudeUsageURL, headers)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return statusError(status)
	}
	out.Windows = parseClaudeUsage(body)
	if extra := parseClaudeExtraUsage(body); extra != "" {
		out.addDetail("额外用量", extra)
	}
	// Plan type is a best-effort second call; ignore its failure.
	if profile, s, perr := quotaGet(ctx, ua, claudeProfileURL, headers); perr == nil && s >= 200 && s < 300 {
		if plan := parseClaudePlan(profile); plan != "" {
			out.PlanType = plan
			out.addDetail("套餐", plan)
		}
	}
	return nil
}

func fetchCodexQuota(ctx context.Context, ua UpstreamAuth, out *AccountQuota) error {
	body, status, err := quotaGet(ctx, ua, codexUsageURL, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   codexUsageUserAgent,
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return statusError(status)
	}
	root := gjson.ParseBytes(body)
	if plan := codexPlanLabel(firstResult(root, "plan_type", "planType").String()); plan != "" {
		out.PlanType = plan
		out.addDetail("套餐", plan)
	}
	if credits := firstResult(root.Get("rate_limit_reset_credits"), "available_count", "availableCount"); credits.Exists() {
		out.ResetCredits = int(credits.Float())
		out.CanReset = out.ResetCredits > 0
		out.addDetail("可用重置额度", trimFloat(credits.Float()))
	}
	out.Windows = parseCodexRateLimit(root.Get("rate_limit"), "")
	// Code-review limits are a separate bucket some plans expose.
	if cr := parseCodexRateLimit(firstResult(root, "code_review_rate_limit", "codeReviewRateLimit"), "代码审查 · "); len(cr) > 0 {
		out.Windows = append(out.Windows, cr...)
	}
	return nil
}

// claudeWindowLabels maps the Claude usage window keys to customer-facing
// labels, in display order. Matches the panel's CLAUDE_USAGE_WINDOW_KEYS.
var claudeWindowLabels = []struct{ key, label string }{
	{"five_hour", "5 小时"},
	{"seven_day", "7 天"},
	{"seven_day_oauth_apps", "7 天 · OAuth 应用"},
	{"seven_day_opus", "7 天 · Opus"},
	{"seven_day_sonnet", "7 天 · Sonnet"},
	{"seven_day_cowork", "7 天 · 协作"},
}

// parseClaudeUsage turns the Anthropic oauth/usage payload into normalized
// windows. Each window carries "utilization" (0..100 used) and "resets_at".
func parseClaudeUsage(body []byte) []QuotaWindow {
	root := gjson.ParseBytes(body)
	windows := make([]QuotaWindow, 0, len(claudeWindowLabels))
	for _, w := range claudeWindowLabels {
		node := root.Get(w.key)
		if !node.Exists() || !node.Get("utilization").Exists() {
			continue
		}
		used := node.Get("utilization").Float()
		win := QuotaWindow{ID: w.key, Label: w.label}
		setUsed(&win, used)
		if ts := node.Get("resets_at"); ts.Exists() {
			if t := parseResetTime(ts.String()); t != nil {
				win.ResetsAt = t
			}
		}
		windows = append(windows, win)
	}
	return windows
}

// parseClaudeExtraUsage renders the paid extra-usage line ("$used / $limit")
// from the usage payload, or "" when extra usage is disabled. Credit amounts are
// reported in cents.
func parseClaudeExtraUsage(body []byte) string {
	extra := gjson.ParseBytes(body).Get("extra_usage")
	if !extra.Exists() || !extra.Get("is_enabled").Bool() {
		return ""
	}
	used := extra.Get("used_credits").Float() / 100
	limit := extra.Get("monthly_limit").Float() / 100
	return fmt.Sprintf("$%.2f / $%.2f", used, limit)
}

// parseClaudePlan derives the subscription tier from the oauth/profile payload.
func parseClaudePlan(body []byte) string {
	root := gjson.ParseBytes(body)
	if root.Get("account.has_claude_max").Bool() {
		return "Max"
	}
	if root.Get("account.has_claude_pro").Bool() {
		return "Pro"
	}
	orgType := strings.ToLower(root.Get("organization.organization_type").String())
	subStatus := strings.ToLower(root.Get("organization.subscription_status").String())
	if orgType == "claude_team" && subStatus == "active" {
		return "Team"
	}
	if root.Get("account.has_claude_max").Exists() && root.Get("account.has_claude_pro").Exists() {
		return "Free"
	}
	return ""
}

// codexPlanLabel maps a Codex plan_type to a display label.
func codexPlanLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "pro":
		return "Pro"
	case "prolite", "pro-lite", "pro_lite":
		return "Pro Lite"
	case "plus":
		return "Plus"
	case "team":
		return "Team"
	case "free":
		return "Free"
	default:
		return raw
	}
}

// parseCodexUsage turns a ChatGPT wham/usage payload into normalized windows.
// Thin wrapper over parseCodexRateLimit for callers holding the raw body.
func parseCodexUsage(body []byte) []QuotaWindow {
	return parseCodexRateLimit(gjson.ParseBytes(body).Get("rate_limit"), "")
}

// parseCodexRateLimit normalizes one Codex rate_limit block. The primary window
// is the ~5h limit; the secondary window is weekly (or monthly for team plans).
// labelPrefix distinguishes buckets (e.g. code review) in the UI.
func parseCodexRateLimit(rl gjson.Result, labelPrefix string) []QuotaWindow {
	if !rl.Exists() {
		return nil
	}
	windows := make([]QuotaWindow, 0, 2)
	if w := parseCodexWindow(rl.Get("primary_window"), rl.Get("primaryWindow"), "primary", labelPrefix); w != nil {
		windows = append(windows, *w)
	}
	if w := parseCodexWindow(rl.Get("secondary_window"), rl.Get("secondaryWindow"), "secondary", labelPrefix); w != nil {
		windows = append(windows, *w)
	}
	return windows
}

func parseCodexWindow(snake, camel gjson.Result, id, labelPrefix string) *QuotaWindow {
	node := snake
	if !node.Exists() {
		node = camel
	}
	if !node.Exists() {
		return nil
	}
	usedNode := firstResult(node, "used_percent", "usedPercent")
	win := QuotaWindow{ID: labelPrefix + id}
	// Window length decides the label: ~5h primary, weekly/monthly secondary.
	seconds := firstResult(node, "limit_window_seconds", "limitWindowSeconds").Float()
	win.Label = labelPrefix + codexWindowLabel(id, seconds)
	if usedNode.Exists() {
		setUsed(&win, usedNode.Float())
	}
	if t := codexResetTime(node); t != nil {
		win.ResetsAt = t
	}
	return &win
}

func codexWindowLabel(id string, seconds float64) string {
	const day = 24 * 60 * 60
	switch id {
	case "primary":
		return "5 小时"
	default:
		if seconds >= 28*day && seconds <= 31*day {
			return "每月"
		}
		return "每周"
	}
}

// codexResetTime resolves a Codex window reset instant from either an absolute
// epoch (reset_at) or a relative offset (reset_after_seconds).
func codexResetTime(node gjson.Result) *time.Time {
	if v := firstResult(node, "reset_at", "resetAt"); v.Exists() && v.Float() > 0 {
		t := time.Unix(int64(v.Float()), 0).UTC()
		return &t
	}
	if v := firstResult(node, "reset_after_seconds", "resetAfterSeconds"); v.Exists() && v.Float() > 0 {
		t := time.Now().Add(time.Duration(v.Float()) * time.Second).UTC()
		return &t
	}
	return nil
}

// applyAntigravityCredits surfaces the executor-tracked AI credits state as
// detail rows (Antigravity exposes a credit balance, not usage windows).
func applyAntigravityCredits(out *AccountQuota, c *AntigravityCredits) {
	if c == nil || !c.Known {
		out.Note = "该账号尚未使用，余额待首次调用后刷新"
		return
	}
	if c.Available {
		out.addDetail("状态", "可用")
	} else {
		out.addDetail("状态", "已用尽")
	}
	out.addDetail("剩余额度", trimFloat(c.CreditAmount))
	if c.MinCreditAmount > 0 {
		out.addDetail("单次最低消耗", trimFloat(c.MinCreditAmount))
	}
	if !c.UpdatedAt.IsZero() {
		out.addDetail("更新时间", c.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
}

func quotaGet(ctx context.Context, ua UpstreamAuth, url string, headers map[string]string) ([]byte, int, error) {
	return quotaDo(ctx, ua, http.MethodGet, url, headers, nil)
}

// quotaDo performs the live upstream request, injecting the bearer token and
// honoring the account proxy. It intentionally sets no client timeout:
// cancellation is driven by the caller's context (a bounded control-plane
// deadline), consistent with the project's "no timeouts on established upstream
// connections" rule.
func quotaDo(ctx context.Context, ua UpstreamAuth, method, url string, headers map[string]string, body []byte) ([]byte, int, error) {
	token := strings.TrimSpace(ua.Token)
	if token == "" {
		return nil, 0, fmt.Errorf("账号令牌不可用（可能需要重新登录上游）")
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Transport: quotaTransport(ua.ProxyURL)}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求失败")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取响应失败")
	}
	return data, resp.StatusCode, nil
}

func quotaTransport(proxyURL string) http.RoundTripper {
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		if tr, _, err := proxyutil.BuildHTTPTransport(proxyURL); err == nil && tr != nil {
			return tr
		}
	}
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
		clone := base.Clone()
		clone.Proxy = nil
		return clone
	}
	return &http.Transport{Proxy: nil}
}

func setUsed(w *QuotaWindow, used float64) {
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	remaining := 100 - used
	w.UsedPercent = &used
	w.RemainingPercent = &remaining
}

func firstResult(node gjson.Result, keys ...string) gjson.Result {
	for _, k := range keys {
		if v := node.Get(k); v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

func parseResetTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func statusError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("上游拒绝（令牌可能已过期）")
	default:
		return fmt.Errorf("上游返回 %d", status)
	}
}

func trimFloat(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
