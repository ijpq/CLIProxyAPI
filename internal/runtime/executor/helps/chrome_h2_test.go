package helps

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// h2Pipe returns a connected pair of TCP loopback conns. TCP (unlike net.Pipe)
// is buffered, which avoids synchronous-write deadlocks during the HTTP/2
// handshake.
func h2Pipe(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, errAccept := ln.Accept()
		ch <- result{c, errAccept}
	}()

	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	return client, r.conn
}

// serveH2 runs an HTTP/2 server (prior-knowledge, no TLS) on conn.
func serveH2(conn net.Conn, handler http.Handler) {
	(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: handler})
}

func newChromeH2ConnForTest(t *testing.T, conn net.Conn) *chromeH2Conn {
	t.Helper()
	cc, err := newChromeH2Conn(conn)
	if err != nil {
		t.Fatalf("newChromeH2Conn: %v", err)
	}
	return cc
}

func doGet(t *testing.T, cc *chromeH2Conn, rawURL string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	return resp, string(body)
}

// TestChromeH2Fingerprint asserts the on-wire SETTINGS, connection
// WINDOW_UPDATE and request pseudo-header order match Chrome.
func TestChromeH2Fingerprint(t *testing.T) {
	client, server := h2Pipe(t)
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	cc := newChromeH2ConnForTest(t, client)

	sf := http2.NewFramer(server, server)
	sf.ReadMetaHeaders = hpack.NewDecoder(chromeH2HeaderTableSize, nil)

	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(server, preface); err != nil {
		t.Fatalf("read preface: %v", err)
	}
	if string(preface) != http2.ClientPreface {
		t.Fatalf("preface = %q, want client preface", preface)
	}

	// First frame: SETTINGS with Chrome's exact values and order.
	frame, err := sf.ReadFrame()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	settings, ok := frame.(*http2.SettingsFrame)
	if !ok {
		t.Fatalf("first frame = %T, want *SettingsFrame", frame)
	}
	var got []http2.Setting
	_ = settings.ForeachSetting(func(s http2.Setting) error {
		got = append(got, s)
		return nil
	})
	want := []http2.Setting{
		{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: 6291456},
		{ID: http2.SettingMaxHeaderListSize, Val: 262144},
	}
	if len(got) != len(want) {
		t.Fatalf("settings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("settings[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}

	// Second frame: connection-level WINDOW_UPDATE(0, 15663105).
	frame, err = sf.ReadFrame()
	if err != nil {
		t.Fatalf("read window update: %v", err)
	}
	wu, ok := frame.(*http2.WindowUpdateFrame)
	if !ok {
		t.Fatalf("second frame = %T, want *WindowUpdateFrame", frame)
	}
	if wu.StreamID != 0 || wu.Increment != chromeH2ConnWindowIncrement {
		t.Fatalf("window update = stream %d incr %d, want stream 0 incr %d", wu.StreamID, wu.Increment, chromeH2ConnWindowIncrement)
	}

	// Let the client open a stream, then assert pseudo-header order m,a,s,p.
	_ = sf.WriteSettings()
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com/path?q=1", nil)
		_, _ = cc.RoundTrip(req)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for HEADERS frame")
		}
		frame, err = sf.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		mh, isHeaders := frame.(*http2.MetaHeadersFrame)
		if !isHeaders {
			continue
		}
		var order []string
		for _, pf := range mh.PseudoFields() {
			order = append(order, pf.Name)
		}
		wantOrder := []string{":method", ":authority", ":scheme", ":path"}
		if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
			t.Fatalf("pseudo-header order = %v, want %v", order, wantOrder)
		}
		return
	}
}

func TestChromeH2RoundTrip(t *testing.T) {
	client, server := h2Pipe(t)
	defer func() { _ = client.Close() }()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Method", r.Method)
			_, _ = w.Write(body)
		default:
			_, _ = w.Write([]byte("hello"))
		}
	})
	go serveH2(server, handler)

	cc := newChromeH2ConnForTest(t, client)

	resp, body := doGet(t, cc, "https://example.com/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	if body != "hello" {
		t.Fatalf("GET body = %q, want %q", body, "hello")
	}

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/echo", strings.NewReader("ping-pong"))
	req.ContentLength = int64(len("ping-pong"))
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("POST round trip: %v", err)
	}
	body2, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body2) != "ping-pong" {
		t.Fatalf("POST echo body = %q, want %q", body2, "ping-pong")
	}
	if got := resp.Header.Get("X-Method"); got != http.MethodPost {
		t.Fatalf("X-Method = %q, want POST", got)
	}
}

func TestChromeH2StreamingResponse(t *testing.T) {
	client, server := h2Pipe(t)
	defer func() { _ = client.Close() }()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer is not a flusher")
			return
		}
		for i := 0; i < 3; i++ {
			_, _ = fmt.Fprintf(w, "chunk-%d\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	})
	go serveH2(server, handler)

	cc := newChromeH2ConnForTest(t, client)
	_, body := doGet(t, cc, "https://example.com/stream")
	want := "chunk-0\nchunk-1\nchunk-2\n"
	if body != want {
		t.Fatalf("stream body = %q, want %q", body, want)
	}
}

// TestChromeH2LargeBodies exercises flow control on both directions: a request
// body and a response body that each span many DATA frames and exceed the
// initial 64 KiB stream window.
func TestChromeH2LargeBodies(t *testing.T) {
	client, server := h2Pipe(t)
	defer func() { _ = client.Close() }()

	const size = 512 * 1024
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server read body: %v", err)
			return
		}
		_, _ = w.Write(body)
	})
	go serveH2(server, handler)

	cc := newChromeH2ConnForTest(t, client)

	payload := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/echo", bytes.NewReader(payload))
	req.ContentLength = int64(len(payload))
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	_ = resp.Body.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo body mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestChromeH2Concurrent(t *testing.T) {
	client, server := h2Pipe(t)
	defer func() { _ = client.Close() }()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	})
	go serveH2(server, handler)

	cc := newChromeH2ConnForTest(t, client)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/p/%d", i)
			resp, body := doGet(t, cc, "https://example.com"+path)
			if resp.StatusCode != http.StatusOK || body != path {
				errs <- fmt.Errorf("req %d: status %d body %q", i, resp.StatusCode, body)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
