package billing

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestParseClaudeUsage(t *testing.T) {
	body := []byte(`{
		"five_hour":  {"utilization": 42.5, "resets_at": "2026-07-10T12:00:00Z"},
		"seven_day":  {"utilization": 10,   "resets_at": "2026-07-16T00:00:00Z"},
		"seven_day_opus": {"utilization": 0},
		"extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 100}
	}`)
	windows := parseClaudeUsage(body)
	if len(windows) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(windows))
	}
	first := windows[0]
	if first.ID != "five_hour" {
		t.Fatalf("expected first window five_hour, got %q", first.ID)
	}
	if first.UsedPercent == nil || *first.UsedPercent != 42.5 {
		t.Fatalf("expected used 42.5, got %v", first.UsedPercent)
	}
	if first.RemainingPercent == nil || *first.RemainingPercent != 57.5 {
		t.Fatalf("expected remaining 57.5, got %v", first.RemainingPercent)
	}
	if first.ResetsAt == nil {
		t.Fatalf("expected resets_at parsed")
	}
	// A window without utilization must be skipped even if the key exists.
	for _, w := range windows {
		if w.ID == "seven_day_oauth_apps" {
			t.Fatalf("unexpected window for missing key")
		}
	}
}

func TestParseCodexUsage(t *testing.T) {
	body := []byte(`{
		"plan_type": "pro",
		"rate_limit": {
			"primary_window":   {"used_percent": 30, "reset_after_seconds": 3600, "limit_window_seconds": 18000},
			"secondary_window": {"used_percent": 80, "reset_at": 1800000000, "limit_window_seconds": 604800}
		}
	}`)
	windows := parseCodexUsage(body)
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}
	if windows[0].ID != "primary" || windows[0].Label != "5 小时" {
		t.Fatalf("unexpected primary window: %+v", windows[0])
	}
	if windows[0].RemainingPercent == nil || *windows[0].RemainingPercent != 70 {
		t.Fatalf("expected primary remaining 70, got %v", windows[0].RemainingPercent)
	}
	if windows[0].ResetsAt == nil {
		t.Fatalf("expected primary reset from reset_after_seconds")
	}
	if windows[1].Label != "每周" {
		t.Fatalf("expected weekly secondary label, got %q", windows[1].Label)
	}
	if windows[1].ResetsAt == nil {
		t.Fatalf("expected secondary reset from reset_at")
	}
}

func TestParseCodexUsageEmpty(t *testing.T) {
	if got := parseCodexUsage([]byte(`{}`)); len(got) != 0 {
		t.Fatalf("expected no windows for empty payload, got %d", len(got))
	}
}

func TestApplyAntigravityCredits(t *testing.T) {
	var q AccountQuota
	applyAntigravityCredits(&q, &AntigravityCredits{Known: true, Available: true, CreditAmount: 12.5, MinCreditAmount: 0.25})
	if len(q.Details) < 2 {
		t.Fatalf("expected credit detail rows, got %+v", q.Details)
	}
	var haveRemaining bool
	for _, d := range q.Details {
		if d.Label == "剩余额度" && d.Value == "12.5" {
			haveRemaining = true
		}
	}
	if !haveRemaining {
		t.Fatalf("expected remaining credit detail, got %+v", q.Details)
	}

	q = AccountQuota{}
	applyAntigravityCredits(&q, nil)
	if q.Note == "" {
		t.Fatalf("expected refreshing note for nil credits")
	}
}

func TestParseClaudeExtraUsageAndPlan(t *testing.T) {
	usage := []byte(`{"extra_usage":{"is_enabled":true,"monthly_limit":5000,"used_credits":150}}`)
	if got := parseClaudeExtraUsage(usage); got != "$1.50 / $50.00" {
		t.Fatalf("unexpected extra usage: %q", got)
	}
	if got := parseClaudeExtraUsage([]byte(`{"extra_usage":{"is_enabled":false}}`)); got != "" {
		t.Fatalf("expected empty extra usage when disabled, got %q", got)
	}
	if got := parseClaudePlan([]byte(`{"account":{"has_claude_max":true,"has_claude_pro":false}}`)); got != "Max" {
		t.Fatalf("expected Max plan, got %q", got)
	}
	if got := parseClaudePlan([]byte(`{"account":{"has_claude_max":false,"has_claude_pro":true}}`)); got != "Pro" {
		t.Fatalf("expected Pro plan, got %q", got)
	}
}

func TestParseCodexCodeReviewPrefix(t *testing.T) {
	body := []byte(`{"code_review_rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}`)
	windows := parseCodexRateLimit(gjson.ParseBytes(body).Get("code_review_rate_limit"), "代码审查 · ")
	if len(windows) != 1 {
		t.Fatalf("expected 1 code-review window, got %d", len(windows))
	}
	if windows[0].Label != "代码审查 · 5 小时" {
		t.Fatalf("unexpected code-review label: %q", windows[0].Label)
	}
}
