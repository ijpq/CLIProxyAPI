package helps

import (
	"net"
	"os"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

// TestNodeJSHelloSpec_LiveHandshake performs a real TLS handshake against
// cloudcode-pa.googleapis.com using the Node.js spec. Skipped unless the
// CLIPROXY_NETWORK_TESTS env var is set so CI without outbound network
// does not flake. The point of this test is to catch spec bugs that make
// the handshake itself fail (e.g. unsupported extension, bad signature
// algorithm list) — it does NOT verify ja3 accuracy.
func TestNodeJSHelloSpec_LiveHandshake(t *testing.T) {
	if os.Getenv("CLIPROXY_NETWORK_TESTS") == "" {
		t.Skip("set CLIPROXY_NETWORK_TESTS=1 to run network-dependent tests")
	}

	conn, err := net.DialTimeout("tcp", "cloudcode-pa.googleapis.com:443", 10*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	uc := tls.UClient(conn, &tls.Config{ServerName: "cloudcode-pa.googleapis.com"}, tls.HelloCustom)
	spec := nodeJSHelloSpec()
	if err := uc.ApplyPreset(&spec); err != nil {
		t.Fatalf("apply preset: %v", err)
	}
	if err := uc.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	state := uc.ConnectionState()
	if state.Version < tls.VersionTLS12 {
		t.Fatalf("negotiated TLS version %x below TLS 1.2", state.Version)
	}
	if state.NegotiatedProtocol != "h2" && state.NegotiatedProtocol != "http/1.1" {
		t.Fatalf("unexpected ALPN %q", state.NegotiatedProtocol)
	}
}
