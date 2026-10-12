package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/net/proxy"
)

func TestUtlsRoundTripperDelegatesPlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			t.Error("plaintext request unexpectedly used TLS")
		}
		_, _ = io.WriteString(w, "plain-http-ok")
	}))
	defer server.Close()

	client := NewOpenAIHttpClient(&config.SDKConfig{ProxyURL: "direct"})
	resp, errGet := client.Get(server.URL)
	if errGet != nil {
		t.Fatalf("plaintext request failed: %v", errGet)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read plaintext response: %v", errRead)
	}
	if got := string(body); got != "plain-http-ok" {
		t.Fatalf("plaintext response = %q, want %q", got, "plain-http-ok")
	}
}

func TestUtlsRoundTripperHTTPSNegotiatesHTTPProtocols(t *testing.T) {
	tests := []struct {
		name        string
		enableHTTP2 bool
		wantMajor   int
	}{
		{name: "HTTP/1.1 fallback", wantMajor: 1},
		{name: "HTTP/2", enableHTTP2: true, wantMajor: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "https-ok")
			}))
			server.EnableHTTP2 = test.enableHTTP2
			server.StartTLS()
			defer server.Close()

			rt := newUtlsRoundTripper(&config.SDKConfig{ProxyURL: "direct"})
			rt.insecureSkipVerify = true
			client := &http.Client{Transport: rt}
			resp, errGet := client.Get(server.URL)
			if errGet != nil {
				t.Fatalf("HTTPS request failed: %v", errGet)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.ProtoMajor != test.wantMajor {
				t.Fatalf("response protocol = %s, want HTTP/%d", resp.Proto, test.wantMajor)
			}
			body, errRead := io.ReadAll(resp.Body)
			if errRead != nil {
				t.Fatalf("read HTTPS response: %v", errRead)
			}
			if got := string(body); got != "https-ok" {
				t.Fatalf("HTTPS response = %q, want %q", got, "https-ok")
			}
		})
	}
}

func TestUtlsRoundTripperRejectsInvalidRequestURLs(t *testing.T) {
	rt := newUtlsRoundTripper(nil)
	tests := []struct {
		name    string
		req     *http.Request
		wantErr string
	}{
		{name: "nil request", wantErr: "codex utls: request URL is required"},
		{name: "missing URL", req: &http.Request{}, wantErr: "codex utls: request URL is required"},
		{name: "unsupported scheme", req: &http.Request{URL: &url.URL{Scheme: "ftp", Host: "example.com"}}, wantErr: `codex utls: unsupported URL scheme "ftp"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, errRoundTrip := rt.RoundTrip(test.req)
			if errRoundTrip == nil || errRoundTrip.Error() != test.wantErr {
				t.Fatalf("RoundTrip() error = %v, want %q", errRoundTrip, test.wantErr)
			}
		})
	}
}

func TestNewUtlsRoundTripperProxyModes(t *testing.T) {
	tests := []struct {
		name             string
		proxyURL         string
		wantDefaultHTTP  bool
		wantDirectDialer bool
		wantHTTPProxy    string
		wantCustomDial   bool
		wantTLSDial      bool
	}{
		{name: "inherit", wantDefaultHTTP: true, wantDirectDialer: true},
		{name: "direct", proxyURL: "direct", wantDirectDialer: true},
		{name: "SOCKS5", proxyURL: "socks5://proxy.example.com:1080", wantCustomDial: true},
		{name: "HTTP", proxyURL: "http://proxy.example.com:8080", wantHTTPProxy: "http://proxy.example.com:8080"},
		{name: "HTTPS", proxyURL: "https://proxy.example.com:8443", wantHTTPProxy: "https://proxy.example.com:8443", wantTLSDial: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := newUtlsRoundTripper(&config.SDKConfig{ProxyURL: test.proxyURL})
			if test.wantDefaultHTTP {
				if rt.httpTransport != http.DefaultTransport {
					t.Fatalf("HTTP transport = %T, want inherited http.DefaultTransport", rt.httpTransport)
				}
			} else {
				transport, ok := rt.httpTransport.(*http.Transport)
				if !ok || transport == nil {
					t.Fatalf("HTTP transport = %T, want *http.Transport", rt.httpTransport)
				}
				if test.wantHTTPProxy == "" {
					if transport.Proxy != nil {
						t.Fatal("HTTP transport unexpectedly retained a proxy function")
					}
				} else {
					if transport.Proxy == nil {
						t.Fatal("HTTP transport proxy function is nil")
					}
					proxyTarget, errProxy := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "target.example.com"}})
					if errProxy != nil {
						t.Fatalf("resolve HTTP proxy: %v", errProxy)
					}
					if proxyTarget == nil || proxyTarget.String() != test.wantHTTPProxy {
						t.Fatalf("HTTP proxy = %v, want %q", proxyTarget, test.wantHTTPProxy)
					}
				}
				if test.wantCustomDial && transport.DialContext == nil {
					t.Fatal("SOCKS transport DialContext is nil")
				}
				if test.wantTLSDial && transport.DialTLSContext == nil {
					t.Fatal("HTTPS proxy transport DialTLSContext is nil")
				}
			}

			if test.wantDirectDialer {
				if rt.dialer != proxy.Direct {
					t.Fatalf("TLS dialer = %T, want proxy.Direct", rt.dialer)
				}
			} else if rt.dialer == nil || rt.dialer == proxy.Direct {
				t.Fatalf("TLS dialer = %#v, want configured proxy dialer", rt.dialer)
			}
		})
	}
}

func TestUtlsRoundTripperPlainHTTPHonorsRequestContext(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	done := make(chan error, 1)
	go func() {
		_, errRoundTrip := newUtlsRoundTripper(&config.SDKConfig{ProxyURL: "direct"}).RoundTrip(req)
		done <- errRoundTrip
	}()
	<-started
	cancel()
	if errRoundTrip := <-done; errRoundTrip == nil || !strings.Contains(errRoundTrip.Error(), context.Canceled.Error()) {
		t.Fatalf("RoundTrip() error = %v, want context cancellation", errRoundTrip)
	}
}
