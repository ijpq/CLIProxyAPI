// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// ClientProfile describes a stable per-credential OS/arch fingerprint used
// to render upstream User-Agent strings. Fields use the conventions of each
// client family: Antigravity uses Node-style platform tokens
// ("darwin"/"linux"/"win32") and Node-style arches ("arm64"/"x64");
// Gemini CLI uses the same Node tokens plus a Node runtime version.
type ClientProfile struct {
	OS      string
	Arch    string
	CLIVer  string
	NodeVer string
}

// antigravityProfilePool is a small pool of realistic Antigravity OS/arch
// combinations. Two credentials hashed to different entries will render
// different darwin/arm64 vs linux/x64 etc. UAs even with the same Antigravity
// version, so a single shared proxy IP does not collapse into one fingerprint.
var antigravityProfilePool = []ClientProfile{
	{OS: "darwin", Arch: "arm64"},
	{OS: "darwin", Arch: "x64"},
	{OS: "linux", Arch: "x64"},
	{OS: "win32", Arch: "x64"},
}

// geminiCLIProfilePool pairs realistic GeminiCLI versions with Node runtime
// versions and platforms observed on common developer setups.
var geminiCLIProfilePool = []ClientProfile{
	{OS: "darwin", Arch: "arm64", CLIVer: "0.34.0", NodeVer: "v22.19.0"},
	{OS: "darwin", Arch: "x64", CLIVer: "0.34.0", NodeVer: "v22.19.0"},
	{OS: "linux", Arch: "x64", CLIVer: "0.34.0", NodeVer: "v22.21.1"},
	{OS: "win32", Arch: "x64", CLIVer: "0.34.0", NodeVer: "v20.18.0"},
	{OS: "darwin", Arch: "arm64", CLIVer: "0.33.0", NodeVer: "v22.19.0"},
	{OS: "linux", Arch: "x64", CLIVer: "0.33.0", NodeVer: "v20.18.0"},
}

// stableProfileIndex maps an arbitrary seed string to a stable index in
// [0, n). Using SHA-256 of the seed guarantees the same credential always
// resolves to the same profile across restarts, which is essential: a
// fingerprint that drifts between requests is itself a signal.
func stableProfileIndex(seed string, n int) int {
	if n <= 0 {
		return 0
	}
	s := strings.TrimSpace(seed)
	if s == "" {
		return 0
	}
	h := sha256.Sum256([]byte(s))
	return int(binary.BigEndian.Uint32(h[:4]) % uint32(n))
}

// AntigravityProfileForAuth returns a deterministic Antigravity profile for
// the given auth ID. An empty auth ID returns the first pool entry, which
// preserves legacy behavior (darwin/arm64).
func AntigravityProfileForAuth(authID string) ClientProfile {
	return antigravityProfilePool[stableProfileIndex(authID, len(antigravityProfilePool))]
}

// GeminiCLIProfileForAuth returns a deterministic Gemini CLI profile for the
// given auth ID. An empty auth ID returns the first pool entry.
func GeminiCLIProfileForAuth(authID string) ClientProfile {
	return geminiCLIProfilePool[stableProfileIndex(authID, len(geminiCLIProfilePool))]
}

// CodexProfile describes a per-credential Codex client fingerprint. Codex
// uses macOS-flavored UA tokens ("Mac OS 26.3.1; arm64") rather than the
// Node-style tokens used by Antigravity / Gemini CLI. Terminal is the
// trailing "<app>/<version>" suffix that the Rust codex_cli_rs appends
// after the parenthetical OS/arch group.
type CodexProfile struct {
	CLIVer   string
	OSToken  string
	Arch     string
	Terminal string
}

// codexProfilePool is a small pool of realistic Codex client fingerprints
// observed in the wild. Mac dominates because Codex is primarily distributed
// to macOS developers; one Linux entry is included to cover Linux users
// installing via cargo. The pool intentionally varies CLI version, OS
// version, arch and terminal so two credentials hashed to different entries
// produce visibly different UAs.
var codexProfilePool = []CodexProfile{
	{CLIVer: "0.118.0", OSToken: "Mac OS 26.3.1", Arch: "arm64", Terminal: "iTerm.app/3.6.9"},
	{CLIVer: "0.118.0", OSToken: "Mac OS 26.3.0", Arch: "arm64", Terminal: "iTerm.app/3.6.8"},
	{CLIVer: "0.118.0", OSToken: "Mac OS 26.2.0", Arch: "arm64", Terminal: "Apple_Terminal/453"},
	{CLIVer: "0.117.0", OSToken: "Mac OS 15.6.1", Arch: "arm64", Terminal: "iTerm.app/3.6.9"},
	{CLIVer: "0.118.0", OSToken: "Mac OS 15.6.1", Arch: "x86_64", Terminal: "iTerm.app/3.6.9"},
	{CLIVer: "0.118.0", OSToken: "Mac OS 26.3.1", Arch: "arm64", Terminal: "WezTerm/20240320-124816-836b6e58"},
	{CLIVer: "0.117.0", OSToken: "Linux 6.6.0", Arch: "x86_64", Terminal: "WezTerm/20240320-124816-836b6e58"},
}

// CodexProfileForAuth returns a deterministic Codex profile for the given
// auth ID. An empty auth ID returns the first pool entry (which matches the
// build-time codexUserAgent constant in the executor) so existing tests that
// exercise the no-auth fallback keep passing.
func CodexProfileForAuth(authID string) CodexProfile {
	return codexProfilePool[stableProfileIndex(authID, len(codexProfilePool))]
}

// CodexUserAgentForAuth renders a Codex User-Agent string from the
// per-credential profile. The format matches the real codex_cli_rs output:
// "codex_cli_rs/<ver> (<os-token>; <arch>) <terminal>".
func CodexUserAgentForAuth(authID string) string {
	p := CodexProfileForAuth(authID)
	if p.Terminal == "" {
		return fmt.Sprintf("codex_cli_rs/%s (%s; %s)", p.CLIVer, p.OSToken, p.Arch)
	}
	return fmt.Sprintf("codex_cli_rs/%s (%s; %s) %s", p.CLIVer, p.OSToken, p.Arch, p.Terminal)
}
