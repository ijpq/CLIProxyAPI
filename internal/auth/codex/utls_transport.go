// Package codex provides authentication functionality for OpenAI's Codex API.
// This file implements a custom HTTP transport using utls to bypass
// Cloudflare's TLS fingerprinting on auth.openai.com.
package codex

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"sync"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/chromeh2"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// utlsConn is the subset of a negotiated HTTP connection the round tripper
// needs. It is satisfied by *chromeh2.Conn and the HTTP/1.1 fallback.
type utlsConn interface {
	CanTakeNewRequest() bool
	RoundTrip(*http.Request) (*http.Response, error)
}

// utlsHTTP1Conn adapts an already-handshaken uTLS connection to net/http. OAuth
// requests are infrequent, so the fallback deliberately uses one connection per
// request instead of maintaining a second connection pool.
type utlsHTTP1Conn struct {
	mu        sync.Mutex
	conn      net.Conn
	transport *http.Transport
	used      bool
}

func newUtlsHTTP1Conn(conn net.Conn) *utlsHTTP1Conn {
	cc := &utlsHTTP1Conn{conn: conn}
	cc.transport = &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			cc.mu.Lock()
			defer cc.mu.Unlock()
			if cc.used {
				return nil, fmt.Errorf("codex utls: HTTP/1.1 connection already used")
			}
			cc.used = true
			return cc.conn, nil
		},
	}
	return cc
}

func (c *utlsHTTP1Conn) CanTakeNewRequest() bool { return false }

func (c *utlsHTTP1Conn) RoundTrip(req *http.Request) (*http.Response, error) {
	return c.transport.RoundTrip(req)
}

// utlsRoundTripper implements http.RoundTripper using utls with Chrome
// fingerprint to bypass Cloudflare's TLS fingerprinting on OpenAI domains.
type utlsRoundTripper struct {
	mu                 sync.Mutex
	connections        map[string]utlsConn
	pending            map[string]*sync.Cond
	dialer             proxy.Dialer
	rootCAs            *x509.CertPool
	insecureSkipVerify bool
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

	var rootCAs *x509.CertPool
	var insecureSkipVerify bool
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok && defaultTransport != nil && defaultTransport.TLSClientConfig != nil {
		rootCAs = defaultTransport.TLSClientConfig.RootCAs
		insecureSkipVerify = defaultTransport.TLSClientConfig.InsecureSkipVerify
	}

	return &utlsRoundTripper{
		connections:        make(map[string]utlsConn),
		pending:            make(map[string]*sync.Cond),
		dialer:             dialer,
		rootCAs:            rootCAs,
		insecureSkipVerify: insecureSkipVerify,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(ctx context.Context, host, addr string) (utlsConn, error) {
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

	cc, err := t.createConnection(ctx, host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	if cc.CanTakeNewRequest() {
		t.connections[host] = cc
	} else {
		delete(t.connections, host)
	}
	return cc, nil
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (utlsConn, error) {
	var (
		conn net.Conn
		err  error
	)
	if contextDialer, ok := t.dialer.(proxy.ContextDialer); ok {
		conn, err = contextDialer.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = t.dialer.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		ServerName:         host,
		RootCAs:            t.rootCAs,
		InsecureSkipVerify: t.insecureSkipVerify,
		NextProtos:         []string{"h2", "http/1.1"},
	}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err = tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	switch protocol := tlsConn.ConnectionState().NegotiatedProtocol; protocol {
	case "h2":
		// Use the Chrome-aligned HTTP/2 layer so the OAuth token exchange/refresh
		// presents a Chrome TLS hello and Chrome HTTP/2 fingerprint together.
		cc, errCreate := chromeh2.NewConn(tlsConn)
		if errCreate != nil {
			_ = tlsConn.Close()
			return nil, errCreate
		}
		return cc, nil
	case "", "http/1.1":
		return newUtlsHTTP1Conn(tlsConn), nil
	default:
		_ = tlsConn.Close()
		return nil, fmt.Errorf("codex utls: unsupported negotiated protocol %q", protocol)
	}
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	cc, err := t.getOrCreateConnection(req.Context(), hostname, addr)
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
