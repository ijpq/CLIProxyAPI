package helps

import (
	tls "github.com/refraction-networking/utls"
)

// nodeJSHelloSpec returns a best-effort ClientHelloSpec approximating the
// TLS ClientHello produced by Node.js 22.x linked against OpenSSL 3.0.x.
//
// Antigravity and Gemini CLI both call Google Code Assist via the Node
// https module, so their real-world ja3/ja4 matches OpenSSL, not BoringSSL.
// Forcing a Chrome HelloID on those hosts would create a UA(Node) <-> TLS
// (Chrome+GREASE) mismatch that is itself a fingerprint signal. This spec
// is used instead so the TLS side stays consistent with the UA.
//
// Limitations:
//   - Hand-built from OpenSSL 3.0 defaults; not captured from a live Node
//     handshake. Cipher and signature-algorithm order should match OpenSSL's
//     compile-time default, but extension order may drift between Node
//     minor versions.
//   - No GREASE (OpenSSL does not emit GREASE entries).
//   - FFDHE DH groups are not exposed by utls's CurveID enum, so
//     supported_groups advertises only the ECC curves Node sends. This is
//     a minor deviation from a real Node hello but does not break the
//     handshake against any modern server.
func nodeJSHelloSpec() tls.ClientHelloSpec {
	return tls.ClientHelloSpec{
		TLSVersMin: tls.VersionTLS12,
		TLSVersMax: tls.VersionTLS13,
		CipherSuites: []uint16{
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			0xC024, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384 (not exported by utls)
			0xC028, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			0x003D, // TLS_RSA_WITH_AES_256_CBC_SHA256
			tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		},
		CompressionMethods: []byte{0x00},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0x00}},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{
				tls.X25519,
				tls.CurveP256,
				tls.CurveP384,
				tls.CurveP521,
			}},
			&tls.SessionTicketExtension{},
			// encrypt_then_mac (RFC 7366); utls does not expose a typed
			// extension so emit an empty-payload generic with id 0x0016.
			&tls.GenericExtension{Id: 0x0016},
			&tls.ExtendedMasterSecretExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				tls.ECDSAWithP256AndSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.ECDSAWithP521AndSHA512,
				tls.Ed25519,
				tls.PSSWithSHA256,
				tls.PSSWithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA256,
				tls.PKCS1WithSHA384,
				tls.PKCS1WithSHA512,
				tls.ECDSAWithSHA1,
				tls.PKCS1WithSHA1,
			}},
			&tls.SupportedVersionsExtension{Versions: []uint16{
				tls.VersionTLS13,
				tls.VersionTLS12,
			}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{1}}, // psk_dhe_ke
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{
				{Group: tls.X25519},
			}},
			&tls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}},
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},
		},
	}
}
