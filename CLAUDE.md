# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

The import above supplies the canonical commands, conventions, and per-directory rules. The notes below complement it with fork-specific architecture and constraints that are easy to miss.

## Fork context

This checkout is the `ijpq/CLIProxyAPI` fork of `router-for-me/CLIProxyAPI`. The long-lived commercial work is on `claude/request-logs-billing`; verify the current branch and remotes before rebasing, publishing, or opening a PR. `AGENTS.md` is upstream-authored: its conventions remain authoritative, but parts of its directory map lag this fork (for example, `internal/api/modules/amp/` is absent here and `internal/api/modules/portal/` is the active in-tree module).

The fork's detailed operational and architecture handoff is `docs/DEVELOPER_HANDOFF_CN.md`; billing setup is documented in `docs/billing-guide.md`. User documentation is at <https://help.router-for.me/>. SDK documentation is in `docs/sdk-{usage,advanced,access,watcher}.md`, with an embedding example under `examples/custom-provider/`.

## Commands and validation

The module requires Go 1.26+.

```bash
# First local run
cp config.example.yaml config.yaml
go run ./cmd/server --config config.yaml

# Required after Go changes
gofmt -w .
go test ./...
go test -v -run TestName ./path/to/pkg
go build -o test-output ./cmd/server && rm test-output
git diff --check
```

There is no separate lint configuration; `gofmt` is the formatting gate. The root `go test ./...` does not enter the separate Go modules under `examples/plugin/*/go/`; test a changed plugin module from its own directory.

PR CI (`.github/workflows/pr-test-build.yml`) refreshes model catalogs and compiles the server, but it does **not** run `go test`; local tests are the only test gate. Build metadata (`Version`, `Commit`, `BuildDate`) is injected with inline `-ldflags` in `.github/workflows/release.yaml` and `Dockerfile`; the defaults in `cmd/server/main.go` are suitable for local builds.

If the host lacks Go 1.26, run checks in the matching container while keeping caches outside the repository:

```bash
docker run --rm \
  -v "$PWD:/app" \
  -v /tmp/cliproxy-go-mod-cache:/go/pkg/mod \
  -v /tmp/cliproxy-go-build-cache:/root/.cache/go-build \
  -w /app golang:1.26-bookworm \
  go test ./...
```

If a container build reports `error obtaining VCS status`, configure `/app` as a Git `safe.directory` or use `go build -buildvcs=false` for compile verification; do not change repository ownership to work around it.

## Repository invariants

- **Do not edit `AGENTS.md`.** `.github/workflows/agents-md-guard.yml` auto-closes PRs that modify it. Put fork-specific agent guidance here instead.
- **Treat `internal/translator/**` as PR-blocked.** `.github/workflows/pr-path-guard.yml` fails every PR that touches this tree, including broader changes. Also follow the permission/issue process in `AGENTS.md`; its broader-change allowance does not bypass the CI guard.
- **Use `/v7` in internal imports.** The module is `github.com/router-for-me/CLIProxyAPI/v7`.
- **Do not hand-edit generated model catalogs.** `.github/scripts/refresh-model-catalogs.sh` replaces `internal/registry/models/models.json` and conditionally replaces `codex_client_models.json` from `router-for-me/models` during PR builds.
- **Open PRs against `dev`.** `.github/workflows/auto-retarget-main-pr-to-dev.yml` retargets ordinary PRs opened against `main`.
- **A branch push can publish an image.** Pushes to `main`, `dev`, `claude_update`, or `claude/**` trigger `.github/workflows/dockerhub-branch.yml`, which builds and pushes multi-architecture Docker Hub images.
- **Compose defaults to the upstream image.** `docker-compose.yml` uses `eceasy/cli-proxy-api:latest` unless `CLI_PROXY_IMAGE` is set; use `docker compose up --build` or set the fork image explicitly when validating fork changes.
- **READMEs are multi-language.** When user-facing behavior changes, update `README.md`; keep `README_CN.md` and `README_JA.md` synchronized when practical.
- **Most new files under `docs/` are ignored.** `.gitignore` ignores `docs/*` except `docs/DEVELOPER_HANDOFF_CN.md`; add an explicit negation before expecting a new document to be committed.

## Request pipeline

A proxy request crosses four decoupled layers:

1. **Client-format handlers** in `sdk/api/handlers/{openai,claude,gemini}` accept public API dialects and are mounted from `internal/api/server.go`. `internal/api/protocol_multiplexer.go` and `mux_listener.go` allow one port to serve multiple protocols.
2. **Auth selection and execution orchestration** in `sdk/cliproxy/auth/` resolves model/provider routing, selects an upstream credential, and invokes the registered executor. Model metadata and remote refresh behavior live in `internal/registry/`.
3. **Translators** in `internal/translator/` convert requests and responses between client and upstream formats. Pairings self-register in `internal/translator/init.go` as a `<from-format>/<to-format>` matrix.
4. **Provider executors** in `internal/runtime/executor/` perform upstream network I/O for Codex, Gemini/Vertex/AIStudio, Claude, Antigravity, Kimi, xAI, and generic OpenAI-compatible providers. Codex and xAI also have WebSocket executors. Provider OAuth/credential helpers live under `internal/auth/`; Gemini API-key paths are config-backed, while Vertex credentials use `internal/auth/vertex/`.

The thinking/reasoning pipeline in `internal/thinking/` sits inside this flow and must retain its `canonical ThinkingConfig -> centralized normalization/validation -> provider-specific apply` architecture. Do not bypass it with provider-specific logic in a handler or translator.

## Fork-only Portal, billing, and access control

`cmd/server/billing_wire.go` is the composition root for the commercial layer. When enabled, it creates or reuses the billing store, registers database-backed proxy-key authentication, installs rate/balance/ACL handlers and usage metering, then mounts Portal routes.

- `internal/api/modules/portal/` contains the Portal REST API, JWT middleware, admin routes, and embedded SPA (`static/index.html`, served under `/portal/ui/`).
- `internal/access/db_access/` authenticates inbound proxy traffic using Portal-issued `cpk_` keys stored in PostgreSQL.
- `internal/billing/` implements pricing, balance enforcement, rate limits, metering, top-ups, quota, notifications, and related policy.
- Billing should use its own `BILLING_DATABASE_URL`. Falling back to the shared `PGSTORE_DSN` also moves CPA auth/config storage into PostgreSQL and can make file-backed `auths/` appear to disappear.

Keep the three identity layers separate when debugging: a Portal user, that user's hashed `cpk_` API key, and the upstream CPA OAuth credential selected to execute a request are different records with different lifecycles.

Per-user model/account ACLs must survive all the way to the layer that selects an upstream credential. Changes must cover ordinary HTTP, streaming/SSE, WebSocket, token-count, Home routing, plugin execution, and scheduler fast paths—not only the first Gin handler. Current empty allowlists mean **unrestricted**, not deny-all; changing that requires an explicit three-state migration and regression coverage. Plugin paths that cannot prove which upstream credential they used fail closed when an account allowlist is active.

## Management and web surfaces

Three separate browser surfaces use different credentials:

- `/management.html` is the official Management UI downloaded and served by `internal/managementasset/`; its source belongs in the configured `panel-github-repository`, not this repository. It uses the Management key.
- `/request-logs.html` is this fork's in-tree request-log viewer (`internal/api/request_logs_page.go`). Its data endpoints are under `/v0/management`, so it also uses the Management key.
- `/portal/ui/` is the in-tree commercial Portal SPA and uses a Portal JWT. Its generated `cpk_` keys authenticate proxy requests, not either UI.

Everything under `/v0/management` is separate from proxy traffic. Handlers live in `internal/api/handlers/management/` and SDK wiring is in `sdk/api/management.go`. The `remote-management` config controls exposure: an empty secret key returns 404 for the surface, non-local requests require `allow-remote: true`, and every request—including localhost—must provide the key.

## Network fingerprinting

`internal/runtime/executor/helps/` and `internal/chromeh2/` jointly implement provider-facing uTLS/HTTP2/client-profile behavior. TLS ClientHello, HTTP/2 settings, header order, and User-Agent must remain mutually consistent and stable per credential; changing only one layer can break upstream compatibility. Ordinary `net/http` unit tests cannot validate the complete wire fingerprint, so exercise the real provider path when changing this code.

## Plugin host

`internal/pluginhost/` loads trusted in-process dynamic-library plugins, disabled by default under the `plugins` config block. Loading is build-tag split across `loader_unix.go` (`cgo && (linux || darwin || freebsd)`), `loader_windows.go`, and `loader_unsupported.go` (`!cgo && !windows`); update every relevant variant when loader behavior changes.

The stable plugin ABI is in `sdk/pluginabi/` and `sdk/pluginapi/`. Plugins can register executors (which require a matching auth record for the same provider key), command-line flags, and management routes, but cannot replace existing native flags or routes. Keep executor helper/support code in `internal/runtime/executor/helps/`, not alongside executor implementations.
