package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/net/proxy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

func TestNewCodexAuthWithProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "socks5://proxy.example.com:1080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "direct")

	rt, ok := auth.httpClient.Transport.(*utlsRoundTripper)
	if !ok || rt == nil {
		t.Fatalf("expected *utlsRoundTripper, got %T", auth.httpClient.Transport)
	}
	if rt.dialer != proxy.Direct {
		t.Fatalf("expected direct dialer, got %T", rt.dialer)
	}
}

func TestNewCodexAuthWithProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "socks5://global.example.com:1080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "socks5://override.example.com:1081")

	rt, ok := auth.httpClient.Transport.(*utlsRoundTripper)
	if !ok || rt == nil {
		t.Fatalf("expected *utlsRoundTripper, got %T", auth.httpClient.Transport)
	}
	if rt.dialer == nil || rt.dialer == proxy.Direct {
		t.Fatalf("expected override proxy dialer, got %#v", rt.dialer)
	}
}
