package helps

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// UtlsWebsocketTLSDial returns a NetDialTLSContext function suitable for
// gorilla/websocket's Dialer. It performs the underlying TCP/SOCKS5/HTTP
// CONNECT dial and then completes a Chrome utls TLS handshake, so wss://
// requests carry a browser-like TLS fingerprint while preserving the
// configured proxy semantics.
//
// host is the SNI/target hostname (without port). proxyURL is the same
// proxy URL string accepted elsewhere in the codebase. When proxyURL is
// empty or "direct", the dial is performed without a proxy.
func UtlsWebsocketTLSDial(host, proxyURL string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, err := dialWebsocketRaw(ctx, network, addr, proxyURL)
		if err != nil {
			return nil, err
		}

		serverName := host
		if h, _, splitErr := net.SplitHostPort(addr); splitErr == nil && serverName == "" {
			serverName = h
		}

		tlsConfig := &tls.Config{
			ServerName: serverName,
			NextProtos: []string{"http/1.1"},
		}
		tlsConn := tls.UClient(rawConn, tlsConfig, tls.HelloChrome_Auto)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("utls websocket handshake: %w", err)
		}
		return tlsConn, nil
	}
}

func dialWebsocketRaw(ctx context.Context, network, addr, proxyURL string) (net.Conn, error) {
	netDialer := &net.Dialer{}

	trimmed := strings.TrimSpace(proxyURL)
	if trimmed == "" {
		return netDialer.DialContext(ctx, network, addr)
	}

	setting, errParse := proxyutil.Parse(trimmed)
	if errParse != nil {
		return nil, fmt.Errorf("utls websocket: parse proxy %q: %w", trimmed, errParse)
	}

	switch setting.Mode {
	case proxyutil.ModeDirect, proxyutil.ModeInherit:
		return netDialer.DialContext(ctx, network, addr)
	case proxyutil.ModeProxy:
		// handled below
	default:
		return netDialer.DialContext(ctx, network, addr)
	}

	switch strings.ToLower(setting.URL.Scheme) {
	case "socks5", "socks5h":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			return nil, fmt.Errorf("utls websocket: socks5 dialer: %w", errSOCKS5)
		}
		if ctxDialer, ok := socksDialer.(proxy.ContextDialer); ok {
			return ctxDialer.DialContext(ctx, network, addr)
		}
		return socksDialer.Dial(network, addr)
	case "http", "https":
		return httpConnectDial(ctx, netDialer, setting.URL, addr)
	default:
		return nil, fmt.Errorf("utls websocket: unsupported proxy scheme %q", setting.URL.Scheme)
	}
}

func httpConnectDial(ctx context.Context, netDialer *net.Dialer, proxyURL *url.URL, target string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if proxyURL.Port() == "" {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyAddr = net.JoinHostPort(proxyURL.Hostname(), "443")
		} else {
			proxyAddr = net.JoinHostPort(proxyURL.Hostname(), "80")
		}
	}

	conn, err := netDialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("utls websocket: dial http proxy: %w", err)
	}

	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		connectReq.Header.Set("Proxy-Authorization", "Basic "+basicAuth(username, password))
	}

	if err := connectReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("utls websocket: write CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("utls websocket: read CONNECT response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("utls websocket: CONNECT %s failed: %s", target, resp.Status)
	}
	if br.Buffered() > 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("utls websocket: proxy returned unexpected data after CONNECT")
	}
	return conn, nil
}

func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}
