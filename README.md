# 🔒 Proxy-Over-SMTP

**Proxy-Over-SMTP** is a SOCKS4/5, HTTP and HTTPS proxy tunnel that opens with a fake SMTP session and carries the payload through a selectable XOR or AES-256-GCM stream. This project is inspired by [smtp-tunnel-proxy](https://github.com/x011/smtp-tunnel-proxy).

A **client** exposes one local proxy port that speaks SOCKS4/4a, SOCKS5, HTTP and HTTPS. A **server** answers a fake SMTP handshake, then carries every proxied connection as a multiplexed stream inside a single TCP connection.

---

## ⚠️ Breaking Changes

The command line changed completely. Old command lines no longer work: Cobra rejects single-dash long flags such as `-secret` and `-mode`. Update every service unit, Docker command and script before upgrading.

| Before | Now |
|---|---|
| `-mode server` | `server` subcommand |
| `-mode client` | `client` subcommand |
| `-server ADDR` | `server --listen ADDR` |
| `-client ADDR` | `client --listen ADDR` |
| `-remote ADDR` | `client --remote ADDR` |
| `-secret X` (default `THIS_IS_YOUR_SECRET_WORD`) | `--secret X` or env `PROXY_OVER_SMTP_SECRET`. **Required, no default.** |
| `-log-file PATH` (default `./proxy-over-smtp.log`) | `--log-file PATH`. **Off by default**, logs go to stdout only |
| `AUDIT: ...` plain text lines | `slog` structured lines (`--log-format text` or `json`) |
| `-allow-private` | `server --allow-private` |

**Client and server must be upgraded together.** The tunnel uses smux protocol v2 (per-stream flow control) and a challenge-response handshake. The handshake is now a real RFC 5321 envelope with the proof carried as the `X-PROOF` parameter on `MAIL FROM`, so old and new binaries do not interoperate on any of the three counts. The RFC alignment sweep on top of it — case-insensitive verbs, `HELO`, any EHLO argument, the `250`/`502`/`500`/`501`/`503` reply mapping — changed only error-path replies, so an older client still works against a newer server.

---

## ✨ Why Proxy-Over-SMTP?

*   **🎭 SMTP Disguise:** Every connection opens with a plausible RFC 5321 mail transaction — `220` / `EHLO` / `MAIL FROM` / `RCPT TO` / `DATA` / `354` — with the HMAC proof hidden in an ESMTP parameter. Commands are case-insensitive and `HELO` is accepted as an alias for `EHLO`, the way a real relay answers. A failure gets the reply a real server gives it: `250` for `NOOP`/`RSET`, `502` for `VRFY`/`EXPN`/`HELP`, `500` for a malformed line, an over-long line or a bad proof, `501` for an opening command whose argument is missing, `503` for a command out of order, `221` for `QUIT`. Only an I/O error, a timeout or a peer that closes without a word passes silently, and every reply is the last thing the session sends.
*   **🔐 Selectable Cipher:** `--cipher aes` (default) wraps the tunnel in AES-256-GCM records for confidentiality and integrity; `--cipher xor` keeps a fast rolling-key XOR for obfuscation only.
*   **🤝 Challenge-Response Handshake:** The server sends a fresh nonce and the client answers with an HMAC of the secret. The secret is never sent on the wire, and both stream keys are derived from it per connection.
*   **⚡ Multiplexed Tunnel:** One TCP connection carries many streams via [smux](https://github.com/xtaci/smux), with keepalive, for low latency and fewer handshakes.
*   **🔧 Tuned Sockets:** `TCP_NODELAY`, TCP keepalive, and `SO_REUSEADDR` / `SO_REUSEPORT` on Unix listeners are set in code, and send/receive buffers stay under kernel autotuning so bulk transfers are not capped by a fixed buffer/RTT limit. Nothing to configure: see [Socket options](docs/ARCHITECTURE.md#5-socket-options).
*   **🧦 Many Proxy Protocols, One Port:** SOCKS4/4a, SOCKS5, HTTP (plain and `CONNECT`) and HTTPS (TLS proxy listener) are auto-detected from the first byte. Works with browsers, `curl`, `git`, `apt` and anything that honors `http_proxy` / `https_proxy` or SOCKS.
*   **🛑 Graceful Shutdown:** On `SIGINT` / `SIGTERM` the listener stops and active connections drain until done or `--drain-timeout` (default 30s). A second signal forces exit.
*   **📝 Structured Logs:** `slog` events on stdout (text or JSON), one `tunnel opened` line per proxied connection. Optional log file.
*   **🌱 Twelve-Factor:** All settings from flags or `PROXY_OVER_SMTP_*` env vars. The secret has no default.
*   **📦 Static Binaries:** `CGO_ENABLED=0` builds for Linux, macOS, and Windows, plus a Docker image.

---

## 🏗️ Architecture at a Glance

```mermaid
graph LR
    App["Browser / curl<br/>(SOCKS4/5, HTTP, HTTPS)"] --> Client["Client<br/>:1080"]
    Client -- "fake SMTP handshake<br/>+ XOR / AES-256-GCM<br/>+ smux" --> Server["Server<br/>:465"]
    Server -- "protocol detect + negotiate<br/>per stream" --> Target["Target host"]
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and [docs/WORKFLOWS.md](docs/WORKFLOWS.md) for details.

---

## 🚀 Getting Started

### 📋 Prerequisites

*   **Go** (1.25+)
*   **Make** (For Builds)
*   **GoReleaser** *(Optional, for mass binaries)*
*   **Docker** *(Optional, for containers)*

---

## 🛠️ Deployment

### 🐳 **Using Container**

1.  **Install Docker** following the [official guide](https://docs.docker.com/desktop/).
2.  **Run the server side:**
    ```sh
    docker run -d \
      -p 465:465 \
      -e PROXY_OVER_SMTP_SECRET="change-me" \
      --name proxy-over-smtp-server \
      --rm dimaskiddo/proxy-over-smtp:latest \
      server --listen 0.0.0.0:465
    ```
3.  **Run the client side:**
    ```sh
    docker run -d \
      -p 1080:1080 \
      -e PROXY_OVER_SMTP_SECRET="change-me" \
      --name proxy-over-smtp-client \
      --rm dimaskiddo/proxy-over-smtp:latest \
      client --listen 0.0.0.0:1080 --remote 192.168.1.100:465
    ```
4.  Point your browser or tool at `127.0.0.1:1080` (or your client port) as a SOCKS or HTTP proxy. See [Using the proxy](#using-the-proxy).
5.  Docker sends `SIGKILL` after 10s by default, which cuts the drain. Give it more time than `--drain-timeout`: `docker run --stop-timeout 35 ...`, or `stop_grace_period: 35s` in Compose. In Kubernetes set `terminationGracePeriodSeconds` above the drain timeout.

The image entrypoint is `proxy-over-smtp` and the default command is `server`. That default is an explicit subcommand, so it always wins over `PROXY_OVER_SMTP_MODE`; the variable is for runs whose command line carries no subcommand at all.

### 📦 **Using Pre-Built Binaries**

1.  Download the latest release from the [Releases Page](https://github.com/dimaskiddo/proxy-over-smtp/releases) and extract it.
2.  **Run server and client** (set the secret in the environment so it stays out of `ps` and shell history):

#### 🐧 **Linux / 🍎 macOS**
```sh
chmod 755 proxy-over-smtp
export PROXY_OVER_SMTP_SECRET="change-me"

# Server
./proxy-over-smtp server --listen 0.0.0.0:465

# Client
./proxy-over-smtp client --listen 0.0.0.0:1080 --remote 192.168.1.100:465
```

#### 🪟 **Windows**
*(PowerShell)*
```powershell
$env:PROXY_OVER_SMTP_SECRET = "change-me"

# Server
.\proxy-over-smtp.exe server --listen 0.0.0.0:465

# Client
.\proxy-over-smtp.exe client --listen 0.0.0.0:1080 --remote 192.168.1.100:465
```

### 🏗️ **Build From Source**

```sh
git clone -b master https://github.com/dimaskiddo/proxy-over-smtp.git
cd proxy-over-smtp

make vendor    # Pull vendor packages
make run ARGS="server --secret change-me"   # Run from source
make build     # Build binary for this platform
make release   # (Optional) Mass binaries via GoReleaser, output in dist/
```

---

## 🕹️ Usage

```
proxy-over-smtp <command> [flags]

  server    Run the tunnel server
  client    Run the local proxy client (SOCKS4/5, HTTP, HTTPS)
  update    Update this binary to the latest GitHub release
  version   Print version information (also --version)
```

`version` prints one line, the release tag and the commit it was built from:

```
Proxy-Over-SMTP v0.5.0~c04bfca
```

With no subcommand, `PROXY_OVER_SMTP_MODE` picks the mode instead — useful where the command line is fixed, such as a systemd unit, a container or a Compose service:

```ini
# systemd unit
Environment=PROXY_OVER_SMTP_MODE=server
Environment=PROXY_OVER_SMTP_SECRET=change-me
ExecStart=/usr/local/bin/proxy-over-smtp
```

It accepts `server` or `client` only, and an explicit subcommand always wins. `update` and `version` are never selected this way, so a stray environment variable cannot self-replace the binary.

Server and client use the same binary and the same secret. Every flag can also be set through an environment variable named `PROXY_OVER_SMTP_` plus the flag name in upper case with `-` as `_`. Precedence: flag, then env, then default.

| Flag | Env | Default | Commands | Purpose |
|---|---|---|---|---|
| — | `PROXY_OVER_SMTP_MODE` | empty | server, client | Mode to run when no subcommand is given: `server` or `client`. An explicit subcommand wins |
| `--listen` | `PROXY_OVER_SMTP_LISTEN` | server `0.0.0.0:465`, client `0.0.0.0:1080` | server, client | Listen address |
| `--remote` | `PROXY_OVER_SMTP_REMOTE` | `127.0.0.1:465` | client | Server address the client dials |
| `--secret` | `PROXY_OVER_SMTP_SECRET` | none, **required** | server, client | Shared secret: handshake authentication and stream key master. Prefer the env var |
| `--cipher` | `PROXY_OVER_SMTP_CIPHER` | `aes` | server, client | Stream cipher: `xor` or `aes`. Both ends must match |
| `--max-streams` | `PROXY_OVER_SMTP_MAX_STREAMS` | `128` | server, client | Max concurrent streams per session. Enforced by the server; on the client it is the per-session load at which the pool grows |
| `--pool-min` | `PROXY_OVER_SMTP_POOL_MIN` | `2` | client | Floor the client's session pool shrinks back to. The pool dials on demand, so it never starts here. Client-only |
| `--pool-max` | `PROXY_OVER_SMTP_POOL_MAX` | `8` | client | Max tunnel sessions the client opens under load. Both must satisfy `1 <= min <= max <= 16` |
| `--allow-private` | `PROXY_OVER_SMTP_ALLOW_PRIVATE` | `false` | server | Allow loopback, private and link-local targets (blocked by default) |
| `--tls-cert` | `PROXY_OVER_SMTP_TLS_CERT` | empty | client | PEM certificate. With `--tls-key`, enables the HTTPS (TLS) proxy listener |
| `--tls-key` | `PROXY_OVER_SMTP_TLS_KEY` | empty | client | PEM private key for `--tls-cert`. Both or neither |
| `--drain-timeout` | `PROXY_OVER_SMTP_DRAIN_TIMEOUT` | `30s` | server, client | Max time to let active connections finish on shutdown. `0` closes them at once |
| `--log-level` | `PROXY_OVER_SMTP_LOG_LEVEL` | `info` | all | `debug`, `info`, `warn`, `error` |
| `--log-format` | `PROXY_OVER_SMTP_LOG_FORMAT` | `text` | all | `text` or `json` |
| `--log-file` | `PROXY_OVER_SMTP_LOG_FILE` | empty | all | Also append logs to this file |
| `--auto-update` | `PROXY_OVER_SMTP_AUTO_UPDATE` | `false` | server, client | Check for new releases, install and restart in place |
| `--update-interval` | `PROXY_OVER_SMTP_UPDATE_INTERVAL` | `24h` | server, client | Auto-update check interval, minimum `1h` |

`--update-api` (`PROXY_OVER_SMTP_UPDATE_API`) is hidden: it overrides the release endpoint for tests and mirrors. It is trusted for both the archive and its checksum, so point it only at a source you control.

`--listen` and `--remote` must be `host:port`; a malformed value is rejected at startup instead of at bind time.

### 🧵 Session pool

The client runs several tunnel sessions, not one. Each local connection is pinned to a session for its whole life, and a new session is dialed once every open one is loaded, up to `--pool-max`. Idle sessions are released back down to `--pool-min`.

This raises **aggregate** throughput: N sessions cost N TCP connections and give roughly N times the ceiling of one. It does not raise the speed of a single TCP flow, which still rides one session and is still capped by `window / RTT`. Browsers and download managers open many connections, so a pool is what a fast link actually uses. Rule of thumb: sessions ≈ target Gbps ÷ 1.3, capped at 16.

```sh
proxy-over-smtp client --remote host:465 --pool-min 2 --pool-max 8
```

The server needs no matching flag. One client holding N sessions is N connections to it, and each session is capped by `--max-streams` as usual, so N clients at `--pool-max 8` can present up to 8N session handlers.

### 🧭 Using the proxy

The client port detects the protocol per connection, so one `--listen` serves all of these:

```sh
curl -x socks5h://127.0.0.1:1080 https://example.com    # SOCKS5, DNS on the server
curl -x socks4a://127.0.0.1:1080 https://example.com    # SOCKS4a, DNS on the server
curl -x socks4://127.0.0.1:1080  https://203.0.113.5    # SOCKS4, IPv4 targets only
curl -x http://127.0.0.1:1080    http://example.com     # HTTP proxy, plain request
curl -x http://127.0.0.1:1080    https://example.com    # HTTP proxy, CONNECT
export http_proxy=http://127.0.0.1:1080 https_proxy=http://127.0.0.1:1080
git ls-remote https://github.com/dimaskiddo/proxy-over-smtp
```

**HTTPS proxy (TLS to the client).** Give the client a certificate and `https://` proxy URLs work on the same port. Plain connections keep working.

```sh
proxy-over-smtp client --tls-cert proxy.crt --tls-key proxy.key --remote 192.168.1.100:465
curl -x https://127.0.0.1:1080 --proxy-cacert proxy.crt https://example.com
```

Notes:

- No proxy authentication. SOCKS4 `USERID` and `Proxy-Authorization` are ignored, so bind the client to a trusted interface.
- Plain HTTP requests use one request per connection (`Connection: close`), and only `http://` absolute-form requests are proxied.
- HTTP and SOCKS refusals map as: blocked target is `403` / SOCKS5 `0x02`, timeout is `504`, other failures `502`. SOCKS4 always replies `0x5B`.
- The certificate loads once at start. Restart to rotate it. TLS covers only the hop from your application to the client. The client-to-server hop is protected by `--cipher`.

### 🛑 Graceful shutdown

`SIGINT` / `SIGTERM` stops accepting new connections, then waits for active ones to finish, up to `--drain-timeout`. Logs show `draining active=N`, then `shutdown complete`. At the deadline, or on a second signal, remaining connections are closed and the log shows `drain interrupted, connections closed`. On the server, idle tunnels close at once and the client re-dials on its next connection.

### 🔄 Updating

```sh
proxy-over-smtp version          # Proxy-Over-SMTP v0.5.0~c04bfca
proxy-over-smtp update --check   # Print current and latest version only
proxy-over-smtp update           # Download, verify and replace this binary
proxy-over-smtp update --force   # Reinstall even if up to date, or from a dev build
```

The version line is the release tag plus the commit the binary was built from (`~` separates them). A build with no known commit prints the tag alone, and a source build prints `Proxy-Over-SMTP dev`.

`update` replaces the binary on disk. Running instances keep the old code until restarted.

With `--auto-update`, a running `server` or `client` checks at start and then every `--update-interval`. On a newer release it swaps the binary, drains connections and re-executes itself with the same arguments (same PID on Unix). Dev builds never auto-update.

A newer tag is not the only trigger: when the tag is unchanged but the commit differs, `update` reinstalls and auto-update restarts. That covers a release that was re-tagged or rebuilt in place, where the version string alone would look identical. The comparison needs both commits known, so it is skipped when either side is unknown or the running build is dirty.

Notes:

- Downloads are verified against the release `checksums.txt` (sha256). That proves integrity, not authenticity: releases are not signed.
- The commit comparison is unordered: a tag that moves backwards counts as different too, so a re-tag can cause one reinstall per `--update-interval` until it settles. `update --check` shows both sides before anything is replaced.
- Auto-update can leave server and client on different versions. If a release changes the wire protocol, old and new do not interoperate. Update the server first, then clients.
- Docker: the swap lives in the container layer and a container restart reverts it. Pull a new image instead.
- Windows: the re-executed process is a child, so it detaches from a service manager. Prefer manual `update` plus a restart there.
- The binary location must be writable by the running user.

### 📁 Logs

Logs are an event stream on stdout. Redirect or collect them with your process manager (Docker, systemd, Kubernetes). `--log-file` additionally appends to a file.

```
time=2026-10-07T17:10:31.227+07:00 level=INFO msg="tunnel opened" peer=203.0.113.5:56810 proto=socks5 target=example.com:443
time=2026-10-07T17:10:32.242+07:00 level=WARN msg="target unreachable" proto=socks5 err="dial tcp 10.0.0.1:80: target address not allowed" target=10.0.0.1:80
time=2026-10-07T17:10:33.514+07:00 level=INFO msg="connection closed" peer=127.0.0.1:49822 up="1.049 MB" down="0.000 MB" dur=1.204s
```

The first two lines are the server; the third is the client. `tunnel opened` and `connection closed` are the two audit lines: the server names the destination, the client names the local application that used the proxy and how much it moved. Field order is part of the format on both sides: `peer` first, `target` last on every line that carries it, so the destination starts at the same column down a page of events. The client's `up` and `down` are printed as megabytes, always three decimals and always the `MB` suffix, where MB is 10^6 bytes — the unit on the label is the unit of the number. The client does not parse the request, so its line never contains a target — the destination stays on the server, and a client's log can leave the host without carrying browsing destinations.

`--log-format json` emits the same events as JSON lines. Handshake and protocol rejections appear at `--log-level debug`. The secret is never logged, on either side.

---

## 📈 Performance

The tunnel holds **80% of line rate up to 1 Gbps**, measured over **multiple concurrent streams**. One stream cannot fill a high-latency link: each smux stream carries a fixed 512KB window, so a single stream tops out near `window / RTT` — about 205 Mbps at 20 ms RTT and 82 Mbps at 50 ms. Browsers and download managers already open many connections.

A session aggregates its streams up to a 16MB receive buffer, and `--pool-min`/`--pool-max` add sessions, each with its own buffer, so the client's aggregate bound is `pool × 16MB / RTT`. Rule of thumb: sessions ≈ target Gbps ÷ 1.3, capped at 16. One flow never crosses a session, so a single TCP connection is always capped by `window / RTT` no matter how large the pool.

Measured on one host: ~1.1 Gbps per stream (`aes`), ~1.3 Gbps aggregate for a single session, and ~190 MB/s (`xor`) across two pooled sessions.

Every number, table and caveat: [Architecture → Throughput](docs/ARCHITECTURE.md#throughput). To reproduce them by hand: [Workflows → Verifying Throughput](docs/WORKFLOWS.md#verifying-throughput).

---

## 📚 Documentation

*   [Architecture](docs/ARCHITECTURE.md): module map, handshake, transport stack, design decisions.
*   [Workflows](docs/WORKFLOWS.md): pipeline flows and error recovery.

---

## 🧪 Testing

```sh
make test        # plain go test
make test-race   # race detector; needs CGO, so it is a separate target
```

---

## ✍️ Authors

*   **Dimas Restu Hidayanto** - *Initial Work* - [DimasKiddo](https://github.com/dimaskiddo)

See also the list of [contributors](https://github.com/dimaskiddo/proxy-over-smtp/contributors) who participated in this project.

---

## 🏗️ Dependencies

*   **[Go](https://golang.org/)**
*   **[xtaci/smux](https://github.com/xtaci/smux)** - Stream multiplexer
*   **[spf13/cobra](https://github.com/spf13/cobra)** - CLI framework
*   **[golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys)** - Per-OS socket option constants
*   **[GoReleaser](https://github.com/goreleaser/goreleaser)** - Automated binaries build
*   **[Make](https://www.gnu.org/software/make/)** - Automated execution
*   **[Docker](https://www.docker.com/)** - Containerization

---

## ⚠️ Disclaimer

**DO WITH YOUR OWN RISK (DWYOR)**. This software is provided "as is", without warranty of
any kind, express or implied. Use of this software may involve risks, including but not
limited to service disruption or data loss. The authors are not responsible for any damage
caused by the use of this application.

---

## ⚖️ License

Distributed under the **MIT License**. See [LICENSE](LICENSE) for more information.

---
**Proxy-Over-SMTP** — *Mail traffic on the wire, your proxy underneath.* 🔒📨
