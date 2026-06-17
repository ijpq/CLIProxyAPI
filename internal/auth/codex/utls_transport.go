// Package codex provides authentication functionality for OpenAI's Codex API.
// This file implements a custom HTTP transport using utls to bypass
// Cloudflare's TLS fingerprinting on auth.openai.com.
package codex

import (
	"net/http"
	"strings"
	"sync"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/chromeh2"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// utlsH2Conn is the subset of an HTTP/2 client connection the round tripper
// needs. It is satisfied by *chromeh2.Conn.
type utlsH2Conn interface {
	CanTakeNewRequest() bool
	RoundTrip(*http.Request) (*http.Response, error)
}

// utlsRoundTripper implements http.RoundTripper using utls with Chrome
// fingerprint to bypass Cloudflare's TLS fingerprinting on OpenAI domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]utlsH2Conn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

func newUtlsRoundTripper(cfg *config.SDKConfig) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if cfg != nil {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(cfg.ProxyURL)
		if errBuild != nil {
			log.Errorf("codex utls: failed to configure proxy dialer for %q: %v", cfg.ProxyURL, errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]utlsH2Conn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (utlsH2Conn, error) {
	t.mu.Lock()

	if cc, ok := t.connections[host]; ok && cc.CanTakeNewRequest() {
		t.mu.Unlock()
		return cc, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if cc, ok := t.connections[host]; ok && cc.CanTakeNewRequest() {
			t.mu.Unlock()
			return cc, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	cc, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = cc
	return cc, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (utlsH2Conn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	// Use the Chrome-aligned HTTP/2 layer so the OAuth token exchange/refresh
	// against auth.openai.com presents a Chrome TLS hello AND a Chrome HTTP/2
	// fingerprint, rather than Chrome TLS over Go's standard HTTP/2.
	cc, err := chromeh2.NewConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}

	return cc, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	addr := host
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}
	hostname := req.URL.Hostname()

	cc, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == cc {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// NewOpenAIHttpClient creates an HTTP client that bypasses Cloudflare's TLS
// fingerprinting on OpenAI auth endpoints by using utls with the Chrome
// fingerprint. SDK configuration is honored for proxy settings.
func NewOpenAIHttpClient(cfg *config.SDKConfig) *http.Client {
	return &http.Client{
		Transport: newUtlsRoundTripper(cfg),
	}
}
