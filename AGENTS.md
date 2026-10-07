# Proxy-Over-SMTP — Agent Instructions

SOCKS5 proxy tunneled through a fake SMTP session, with XOR-obfuscated smux multiplexing, to disguise traffic from Deep Packet Inspection (DPI). Never guess protocol behavior — ask when ambiguous.

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
| **CLI** | `internal/cli/` — Cobra commands (`server`, `client`, `version`), env fallback, `slog` logger, graceful drain |
| **Config** | `internal/config/` — `Config` struct, `Validate()` |
| **Tunnel** | `internal/tunnel/` — `Tunnel` struct. Client: local listener, shared smux session, SMTP handshake. Server: SMTP handshake, per-stream SOCKS5 + dial. `mux.go`: smux config |
| **SOCKS5** | `pkg/socks5/` — server-side negotiation (v5, no-auth, CONNECT, IPv4/IPv6/domain) |
| **Relay** | `pkg/relay/` — bidirectional copy with pooled 32KB buffers |
| **XOR Stream** | `pkg/xorstream/` — rolling-key XOR `io.ReadWriter` wrapper (obfuscation only) |

## CLI

```
proxy-over-smtp [--log-level info] [--log-format text] [--log-file ""] <command>
  server   --listen 0.0.0.0:465  --secret S [--allow-private]
  client   --listen 0.0.0.0:1080 --remote 127.0.0.1:465 --secret S
  version  (also --version)
```

Every flag has an env var: `PROXY_OVER_SMTP_` + flag name uppercased, `-` to `_` (e.g. `PROXY_OVER_SMTP_SECRET`). Precedence: flag > env > default. The secret has no default.

---

## Critical Constraints

### Build & Quality
- `CGO_ENABLED=0` always. `gofmt -l .` empty, `go vet ./...` and `go test -race ./...` pass before done.

### Layout
- `cmd/` wiring only. `internal/` app-specific. `pkg/` public and stable: no `internal/` imports, no globals, no logging.
- No new package or abstraction for single-use code.

### Wire Compatibility
- Handshake, XOR, or smux changes must land in client and server together. Old and new binaries do not interoperate — say so in the commit message.

### Context & Concurrency
- `context.Context` first param of long-running functions. Graceful shutdown via signal + context cancel.
- Every goroutine has an exit path. Shared state behind a mutex. Connection goroutines tracked in `Tunnel.conns`.

### Error Handling
- Return errors, never exit outside `cmd/`. Wrap with `fmt.Errorf("...: %w", err)`, lowercase messages. Never ignore silently.
- Check every `io.ReadFull`, `Write`, and `SetDeadline` error.

### Logging
- Injected `*slog.Logger`, structured key/value fields, stdout event stream. Levels: `info` audit events, `warn` failures, `debug` rejections. No `log.Fatal` / `os.Exit` outside `cmd/`. Never log the secret.

### Config
- Twelve-factor: config only from flags and env, no baked-in secret, every new flag gets an env var automatically via `internal/cli/env.go`. No config files.

### I/O
- Stream via `io.Reader` / `io.Writer`, reuse buffers with `sync.Pool`. Deadline on every handshake read.

### Dependencies
- Stdlib first. New third-party deps must be justified, widely adopted, CGO-free.

### Tests
- Stdlib `testing`, table-driven, `_test.go` next to code. `net.Pipe` for protocol tests.

---

## Non-Negotiable Rules

1. **No stubs.** Every function complete, production-ready.
2. **No guessing** on protocol behavior or ambiguous architecture. Pause, state ambiguity, ask.
3. **Never run** `make release`, `make publish`, `git push`, or commit without being asked.
4. **Minimal comments.** Comments describe WHY, never WHAT or HOW.
   - No step-by-step descriptions, no section separators (`// --- Section ---`), no comments restating code.
   - Go doc comments on exported symbols: one line for simple, max three for complex.
   - No comment revealing evasion detail beyond `docs/ARCHITECTURE.md`.

## Known Deviations

Existing code violating the rules above. Fix only when asked.

- `go test -race` needs CGO, which conflicts with `CGO_ENABLED=0`. Run it as `CGO_ENABLED=1 go test -race ./...`; builds stay static.

---

## Directory Tree

```
proxy-over-smtp/
├── cmd/proxy-over-smtp/  # Entry (main.go)
├── internal/
│   ├── cli/              # Cobra commands, env fallback, slog
│   ├── config/           # Config + Validate
│   └── tunnel/           # Client, server, smux config
├── pkg/
│   ├── relay/            # Bidirectional pipe
│   ├── socks5/           # SOCKS5 server negotiation
│   └── xorstream/        # XOR stream (+ test)
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
| `Makefile` | Build targets |
| `.goreleaser.yml` | Release config (darwin/linux/windows × 386/amd64/arm64) |
| `internal/tunnel/` | Client and server implementation |
| `pkg/` | Reusable SOCKS5, relay, XOR stream |
