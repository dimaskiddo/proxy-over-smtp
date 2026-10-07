# 🔒 Proxy-Over-SMTP

**Proxy-Over-SMTP** is a SOCKS4/5, HTTP and HTTPS proxy tunnel that disguises its traffic as an SMTP session and XOR-obfuscates the payload to confuse Deep Packet Inspection (DPI). This project is inspired by [smtp-tunnel-proxy](https://github.com/x011/smtp-tunnel-proxy).

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

**Client and server must be upgraded together.** The tunnel now uses smux protocol v2 (per-stream flow control), so old and new binaries do not interoperate.

---

## ✨ Why Proxy-Over-SMTP?

*   **🎭 SMTP Disguise:** Every connection opens with a plausible `220` / `EHLO` / `DATA` exchange before tunneling starts.
*   **🧩 XOR Obfuscation:** Payload is XORed with a rolling key derived from your shared secret.
*   **⚡ Multiplexed Tunnel:** One TCP connection carries many streams via [smux](https://github.com/xtaci/smux), with keepalive, for low latency and fewer handshakes.
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
    Client -- "fake SMTP handshake<br/>+ XOR + smux" --> Server["Server<br/>:465"]
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

The image entrypoint is `proxy-over-smtp` and the default command is `server`.

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

Server and client use the same binary and the same secret. Every flag can also be set through an environment variable named `PROXY_OVER_SMTP_` plus the flag name in upper case with `-` as `_`. Precedence: flag, then env, then default.

| Flag | Env | Default | Commands | Purpose |
|---|---|---|---|---|
| `--listen` | `PROXY_OVER_SMTP_LISTEN` | server `0.0.0.0:465`, client `0.0.0.0:1080` | server, client | Listen address |
| `--remote` | `PROXY_OVER_SMTP_REMOTE` | `127.0.0.1:465` | client | Server address the client dials |
| `--secret` | `PROXY_OVER_SMTP_SECRET` | none, **required** | server, client | Shared secret (EHLO token and XOR key). Prefer the env var |
| `--allow-private` | `PROXY_OVER_SMTP_ALLOW_PRIVATE` | `false` | server | Allow loopback, private and link-local targets (blocked by default) |
| `--tls-cert` | `PROXY_OVER_SMTP_TLS_CERT` | empty | client | PEM certificate. With `--tls-key`, enables the HTTPS (TLS) proxy listener |
| `--tls-key` | `PROXY_OVER_SMTP_TLS_KEY` | empty | client | PEM private key for `--tls-cert`. Both or neither |
| `--drain-timeout` | `PROXY_OVER_SMTP_DRAIN_TIMEOUT` | `30s` | server, client | Max time to let active connections finish on shutdown. `0` closes them at once |
| `--log-level` | `PROXY_OVER_SMTP_LOG_LEVEL` | `info` | all | `debug`, `info`, `warn`, `error` |
| `--log-format` | `PROXY_OVER_SMTP_LOG_FORMAT` | `text` | all | `text` or `json` |
| `--log-file` | `PROXY_OVER_SMTP_LOG_FILE` | empty | all | Also append logs to this file |
| `--auto-update` | `PROXY_OVER_SMTP_AUTO_UPDATE` | `false` | server, client | Check for new releases, install and restart in place |
| `--update-interval` | `PROXY_OVER_SMTP_UPDATE_INTERVAL` | `24h` | server, client | Auto-update check interval, minimum `1h` |

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
- The certificate loads once at start. Restart to rotate it. TLS covers only the hop from your application to the client. The client-to-server tunnel is unchanged.

### 🛑 Graceful shutdown

`SIGINT` / `SIGTERM` stops accepting new connections, then waits for active ones to finish, up to `--drain-timeout`. Logs show `draining active=N`, then `shutdown complete`. At the deadline, or on a second signal, remaining connections are closed and the log shows `drain interrupted, connections closed`. On the server, idle tunnels close at once and the client re-dials on its next connection.

### 🔄 Updating

```sh
proxy-over-smtp update --check   # Print current and latest version only
proxy-over-smtp update           # Download, verify and replace this binary
proxy-over-smtp update --force   # Reinstall even if up to date, or from a dev build
```

`update` replaces the binary on disk. Running instances keep the old code until restarted.

With `--auto-update`, a running `server` or `client` checks at start and then every `--update-interval`. On a newer release it swaps the binary, drains connections and re-executes itself with the same arguments (same PID on Unix). Dev builds never auto-update.

Notes:

- Downloads are verified against the release `checksums.txt` (sha256). That proves integrity, not authenticity: releases are not signed.
- Auto-update can leave server and client on different versions. If a release changes the wire protocol, old and new do not interoperate. Update the server first, then clients.
- Docker: the swap lives in the container layer and a container restart reverts it. Pull a new image instead.
- Windows: the re-executed process is a child, so it detaches from a service manager. Prefer manual `update` plus a restart there.
- The binary location must be writable by the running user.

### 📁 Logs

Logs are an event stream on stdout. Redirect or collect them with your process manager (Docker, systemd, Kubernetes). `--log-file` additionally appends to a file.

```
time=2026-10-07T17:10:31.227+07:00 level=INFO msg="tunnel opened" peer=203.0.113.5:56810 target=example.com:443 proto=socks5
time=2026-10-07T17:10:32.242+07:00 level=WARN msg="target unreachable" target=10.0.0.1:80 err="dial tcp 10.0.0.1:80: target address not allowed"
```

`--log-format json` emits the same events as JSON lines. Handshake and protocol rejections appear at `--log-level debug`. The secret is never logged.

---

## 📚 Documentation

*   [Architecture](docs/ARCHITECTURE.md): module map, handshake, transport stack, design decisions.
*   [Workflows](docs/WORKFLOWS.md): pipeline flows and error recovery.

---

## 🧪 Testing

```sh
go test ./...
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
*   **[GoReleaser](https://github.com/goreleaser/goreleaser)** - Automated binaries build
*   **[Make](https://www.gnu.org/software/make/)** - Automated execution
*   **[Docker](https://www.docker.com/)** - Containerization

---

## ⚠️ Disclaimer

**DO WITH YOUR OWN RISK (DWYR)**. This software is provided "as is", without warranty of any kind, express or implied. The authors are not responsible for any damage caused by the use of this application.

**This is obfuscation, not encryption.** XOR hides patterns from naive DPI only. The secret is sent in plaintext in the `EHLO` line and there is no TLS. Do not rely on it for confidentiality.

---

## ⚖️ License

Distributed under the **MIT License**. See [LICENSE](LICENSE) for more information.

---
**Proxy-Over-SMTP** — *Mail traffic on the wire, your proxy underneath.* 🔒📨
