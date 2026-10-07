# Proxy-Over-SMTP — Agent Instructions

SOCKS4/5, HTTP and HTTPS proxy tunneled through a fake SMTP session, with a selectable XOR or AES-256-GCM stream over smux multiplexing. Never guess protocol behavior — ask when ambiguous.

> **Research purposes only.** This project exists to study tunneling, obfuscation and stream-cipher design. It is not built, tested or supported for production, commercial or operational use. Run it only on systems you own or are explicitly authorised to test, and obey the law where you are.

---

## Workflow Rules

1. Read `TASKS.md` and project docs before every session to orient to current state.
2. Never rework items marked `[x]` in `TASKS.md` unless explicitly instructed.
3. Update `TASKS.md` immediately after completing a task.
4. Never attempt to write the entire codebase in a single response.

## Skills & Caveman Mode

- **GLOBAL:** All prompts processed as if `"Use caveman mode full"` is injected.
- Before ANY coding task, invoke and read: `using-superpowers`, `karpathy-guidelines`, `caveman`.
- Use `using-superpowers` to route to other relevant skills per task.

---

## Architecture

| Component | Role |
|---|---|
| **Entry** | `cmd/proxy-over-smtp/` — wiring only: signal context, ldflags build info, `cli.Execute` |
| **CLI** | `internal/cli/` — Cobra commands (`server`, `client`, `version`, `update`), env fallback, `slog` logger, graceful drain |
| **Config** | `internal/config/` — `Config` struct, `Validate()` |
| **Update** | `internal/update/` — GitHub release lookup, sha256-verified download, self-replace, re-exec. `ShouldUpdate` adds tag-to-commit resolution (best effort) on top of `Newer`. `assetName` mirrors `.goreleaser.yml` archive names |
| **Tunnel** | `internal/tunnel/` — `Tunnel` struct. Client: local listener, session pool (`--pool-min`/`--pool-max`), SMTP handshake. Server: SMTP handshake, per-stream protocol detection + negotiation + dial, per-session stream cap (`--max-streams`, default 128). Handshake lines capped at 4KB. `Shutdown(ctx)` drains; `spawn` and `Shutdown` share a lock so no handler starts mid-drain, and `RunServer`/`RunClient` after `Shutdown` return an error. `handshake.go`: challenge-response + key derivation + capped line reads. `cipher.go`: XOR/AES stream selection. `mux.go`: smux config. `pool.go`: client session pool (client-only). `socket*.go`: fixed socket options (TCP_NODELAY, reuse, keepalive; send/receive buffers left to kernel autotuning), per-OS |
| **SOCKS5** | `pkg/socks5/` — server-side negotiation (v5, no-auth, CONNECT, IPv4/IPv6/domain) |
| **SOCKS4** | `pkg/socks4/` — SOCKS4/4a request parser and reply writer (CONNECT only) |
| **HTTP proxy** | `pkg/httpproxy/` — CONNECT and absolute-form parser, status writer, forwarder |
| **Relay** | `pkg/relay/` — bidirectional copy with pooled 128KB buffers. `PipeCount` returns the per-direction byte totals for the client's audit line; `Pipe` is the same copy with them dropped |
| **XOR Stream** | `pkg/xorstream/` — rolling-key XOR `io.ReadWriter` wrapper (obfuscation only) |
| **AES Stream** | `pkg/aesstream/` — AES-256-GCM record `io.ReadWriter` wrapper (confidentiality and integrity) |

## CLI

```
proxy-over-smtp [--log-level info] [--log-format text] [--log-file ""] <command>
  server   --listen 0.0.0.0:465  --secret S [--cipher aes] [--max-streams 128] [--allow-private] [--drain-timeout 30s] [--auto-update [--update-interval 24h]]
  client   --listen 0.0.0.0:1080 --remote 127.0.0.1:465 --secret S [--cipher aes] [--max-streams 128] [--pool-min 2 --pool-max 8] [--tls-cert C --tls-key K] [--drain-timeout 30s] [--auto-update [--update-interval 24h]]
  update   [--check] [--force]
  version  (also --version)
```

Every flag has an env var: `PROXY_OVER_SMTP_` + flag name uppercased, `-` to `_` (e.g. `PROXY_OVER_SMTP_SECRET`), including the hidden `--update-api`. Precedence: flag > env > default. The secret has no default. `--cipher` is `xor` or `aes` (default) and must match on both ends. `--listen` and `--remote` are validated as `host:port` at startup. `--pool-min`/`--pool-max` are client-only and must satisfy `1 <= min <= max <= 16`.

### Session Pool
- The pool is client-only: no server flags, and the server ignores the config fields. `New` fills the defaults when both are zero.
- A stream holds a reservation on its slot for life. The reservation is taken under `poolMu` in `pickSlot` and released in `pooledStream.Close`, exactly once. Any new shared state on the stream path must respect that lock, or the shrink scan can close a slot mid-open.
- Keys are never shared between slots: every session runs its own handshake and derives its own keys from its own nonce.
- The pool multiplies aggregate throughput only. Never claim a faster single flow; that is `window / RTT` per session.

---

## Critical Constraints

### Build & Quality
- `CGO_ENABLED=0` always. `gofmt -l .` empty, `go vet ./...` and `go test -race ./...` pass before done.

### Layout
- `cmd/` wiring only. `internal/` app-specific. `pkg/` public and stable: no `internal/` imports, no globals, no logging.
- No new package or abstraction for single-use code.

### Wire Compatibility
- Changing archive names in `.goreleaser.yml` requires updating `assetName` in `internal/update/update.go`, or `update` breaks.
- Handshake, cipher (XOR or AES), or smux changes must land in client and server together. Old and new binaries do not interoperate — say so in the commit message.

### Update Semantics
- The release tag is compared by `X.Y.Z` only (`Newer`); `ShouldUpdate` adds the same-tag-different-commit case. Both live in `internal/update/`; the CLI never compares versions itself.
- An unknown commit (empty, or the linker's `none`) means "no opinion", never "different". A dirty running build skips the commit branch. Neither may trigger a reinstall, or the update loop never settles.
- The commit comparison is unordered and prefix-tolerant with a 7-character floor. A tag that moves backwards reads as different by design; `--update-interval` bounds the flap.
- The commit lookup is best effort. A failure degrades to a tag-only check and is never an error.
- `version` output is one line, `<tag>~<commit>`, with `~<commit>` omitted when the commit is unknown. `buildLabel` in `internal/cli/update.go` is the only formatter of that label; `BuildInfo.String` just prefixes it with the binary name, so an update message and the binary's own line cannot drift apart.

### Context & Concurrency
- `context.Context` first param of long-running functions. Graceful shutdown: signal cancels the run context (stop accepting), then `Tunnel.Shutdown` drains up to `--drain-timeout`. A second signal forces.
- Every goroutine has an exit path. Shared state behind a mutex. Connection goroutines tracked in `Tunnel.conns`.

### Error Handling
- Return errors, never exit outside `cmd/`. Wrap with `fmt.Errorf("...: %w", err)`, lowercase messages. Never ignore silently.
- Check every `io.ReadFull`, `Write`, and `SetDeadline` error.

### Logging
- Injected `*slog.Logger`, structured key/value fields, stdout event stream. Levels: `info` audit events, `warn` failures, `debug` rejections. No `log.Fatal` / `os.Exit` outside `cmd/`. Never log the secret.
- `peer` is per-hop: the server logs the tunnel client plus `target` and `proto`; the client logs only the local application's IP:port with `up`/`down`/`dur`. A client line must never name a target — the client does not parse the request, and a client log has to be safe to ship off-host.

### Config
- Twelve-factor: config only from flags and env, no baked-in secret, every new flag gets an env var automatically via `internal/cli/env.go`. No config files.

### I/O
- Stream via `io.Reader` / `io.Writer`, reuse buffers with `sync.Pool`. Deadline on every handshake read.

### Dependencies
- Stdlib first. New third-party deps must be justified, widely adopted, CGO-free.

### Tests
- Stdlib `testing`, table-driven, `_test.go` next to code. `net.Pipe` for protocol tests.

### Performance
- Target: 80% of line rate up to 1 Gbps, **scoped to multiple streams**. One smux stream is capped at `window / RTT` (512KB window), so never quote a single-connection number for a high-RTT link. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#throughput).
- Aggregate throughput beyond that is the pool's job (`--pool-min`/`--pool-max`), not a bigger single session. `pool × 16MB / RTT` is the client's session-aggregate bound; one flow never crosses a session.
- Benchmarks (`BenchmarkPipe`, `BenchmarkWrite`, `BenchmarkProxyThroughput`, run via `make bench`) report numbers and never assert a rate. No CI perf gate.
- Send and receive buffers stay under kernel autotuning. Do not set `SO_SNDBUF` / `SO_RCVBUF`: a fixed value caps a flow near buffer/RTT.
- Widening the smux session or stream buffers is a wire break, so it needs a joint client+server rollout.

---

## Non-Negotiable Rules

1. **No stubs.** Every function complete, production-ready.
2. **No guessing** on protocol behavior or ambiguous architecture. Pause, state ambiguity, ask.
3. **Never run** `make release`, `make publish`, `git push`, or commit without being asked.
4. **Google Go style comments.**
   - Every package has a package comment (`// Package x ...`, `// Command x ...` for main) in its main file.
   - Every exported identifier and every non-trivial unexported function has a doc comment: a full sentence starting with the identifier name, stating the contract and the why.
   - Function bodies carry WHY comments only: no step-by-step descriptions, no section separators, no comments restating code.
   - No comment revealing evasion detail beyond `docs/ARCHITECTURE.md`.
5. **Docs follow code.** Every feature change updates README, `docs/ARCHITECTURE.md`, `docs/WORKFLOWS.md` and this file in the same change.

## Known Deviations

Existing code violating the rules above. Fix only when asked.

- `go test -race` needs CGO, which conflicts with `CGO_ENABLED=0`. Use `make test-race` (`CGO_ENABLED=1 go test -race ./...`) instead of running the race detector by hand; builds stay static.

---

## Directory Tree

```
proxy-over-smtp/
├── cmd/proxy-over-smtp/  # Entry (main.go)
├── internal/
│   ├── cli/              # Cobra commands, env fallback, slog
│   ├── config/           # Config + Validate
│   ├── tunnel/           # Client, server, handshake, cipher, smux config
│   └── update/           # Self-update: release lookup, verify, replace, re-exec
├── pkg/
│   ├── relay/            # Bidirectional pipe
│   ├── httpproxy/        # HTTP proxy request parser, forwarder (+ test)
│   ├── socks4/           # SOCKS4/4a parser (+ test)
│   ├── socks5/           # SOCKS5 server negotiation
│   ├── xorstream/        # XOR stream (+ test)
│   └── aesstream/        # AES-256-GCM stream (+ test)
├── docs/
│   ├── ARCHITECTURE.md   # Module map, handshake, transport stack, design decisions
│   └── WORKFLOWS.md      # Pipeline flows, error recovery
├── Makefile              # Build targets (build, run, release, publish, clean)
├── .goreleaser.yml       # Cross-platform release config, injects version ldflags
├── Dockerfile            # Multi-stage build
├── AGENTS.md             # Agent instructions (symlinks: CLAUDE.md, GEMINI.md)
├── README.md
├── LICENSE
├── go.mod / go.sum
└── vendor/               # Vendored deps (gitignored)
```

---

## References

| File | Purpose |
|---|---|
| `docs/ARCHITECTURE.md` | Module map, handshake, transport stack, design decisions |
| `docs/WORKFLOWS.md` | Pipeline flows, error recovery |
| `TASKS.md` | Current project state — internal, not in repo |
| `Makefile` | Build targets (`build`, `run`, `test`, `test-race`, `bench`, `bench-short`) |
| `.goreleaser.yml` | Release config (darwin/linux/windows × 386/amd64/arm64) |
| `internal/tunnel/` | Client and server implementation |
| `pkg/` | Reusable SOCKS4/5, HTTP proxy, relay, XOR and AES streams |
