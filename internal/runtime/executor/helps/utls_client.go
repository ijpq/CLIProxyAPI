package helps

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// h2Conn is the subset of an HTTP/2 client connection the round tripper needs.
// It is satisfied by both *chromeH2Conn (Chrome-fingerprinted hosts) and
// *http2.ClientConn (Node-fingerprinted hosts, which keep Go's HTTP/2 layer).
type h2Conn interface {
	CanTakeNewRequest() bool
	RoundTrip(*http.Request) (*http.Response, error)
}

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]h2Conn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]h2Conn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (h2Conn, error) {
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

func (t *utlsRoundTripper) createConnection(host, addr string) (h2Conn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn, err := newUtlsConnForHost(conn, tlsConfig, host)
	if err != nil {
		conn.Close()
		return nil, err
	}

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	// Chrome-fingerprinted hosts also get a Chrome-aligned HTTP/2 layer so the
	// TLS ClientHello and the HTTP/2 SETTINGS/WINDOW_UPDATE/pseudo-header order
	// agree. Node-fingerprinted hosts keep Go's standard HTTP/2 transport.
	if utlsProtectedHosts[strings.ToLower(host)] == fpChrome {
		cc, errH2 := newChromeH2Conn(tlsConn)
		if errH2 != nil {
			tlsConn.Close()
			return nil, errH2
		}
		return cc, nil
	}

	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return cc, nil
}

// newUtlsConnForHost builds a utls connection whose ClientHello matches the
// real client that talks to host. Anthropic / OpenAI hosts get Chrome's
// HelloID; Google Code Assist hosts get the Node.js spec so the TLS layer
// is consistent with the Node UA the executors emit.
func newUtlsConnForHost(conn net.Conn, tlsConfig *tls.Config, host string) (*tls.UConn, error) {
	switch utlsProtectedHosts[strings.ToLower(host)] {
	case fpNodeJS:
		uc := tls.UClient(conn, tlsConfig, tls.HelloCustom)
		spec := nodeJSHelloSpec()
		if err := uc.ApplyPreset(&spec); err != nil {
			return nil, err
		}
		return uc, nil
	default:
		return tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto), nil
	}
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

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

// hostFingerprint selects which ClientHelloID applies to a given upstream.
type hostFingerprint int

const (
	fpChrome hostFingerprint = iota
	fpNodeJS
)

// utlsProtectedHosts maps upstream hosts to the fingerprint that matches their
// real client. Chrome is used for Anthropic/OpenAI (Claude Code / ChatGPT speak
// Chrome BoringSSL); the Node.js spec is used for Google Code Assist hosts
// because both Antigravity and Gemini CLI hit them via the Node https module
// (OpenSSL). Using the wrong fingerprint would create a UA<->TLS mismatch
// that is itself a signal.
var utlsProtectedHosts = map[string]hostFingerprint{
	"api.anthropic.com":                         fpChrome,
	"chatgpt.com":                               fpChrome,
	"auth.openai.com":                           fpChrome,
	"api.openai.com":                            fpChrome,
	"cloudcode-pa.googleapis.com":               fpNodeJS,
	"daily-cloudcode-pa.googleapis.com":         fpNodeJS,
	"daily-cloudcode-pa.sandbox.googleapis.com": fpNodeJS,
}

// fallbackRoundTripper uses utls for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for provider requests that need a Chrome-like TLS fingerprint.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var utlsRT http.RoundTripper = newUtlsRoundTripper(proxyURL)
	var standardTransport http.RoundTripper = http.DefaultTransport
	if proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	} else if ctxRoundTripper != nil {
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
