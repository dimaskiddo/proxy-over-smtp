package tunnel

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/pkg/httpproxy"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/relay"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/socks4"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/socks5"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/xorstream"
)

// errBlocked marks a dial refused by the target ACL so replies can map it to "not allowed".
var errBlocked = errors.New("target address not allowed")

// maxHeader caps the bytes read while parsing a proxy request.
const maxHeader = 64 << 10

// RunServer listens on the configured address and serves tunnel clients until ctx is done.
// It then stops accepting and returns without touching open connections: call Shutdown to
// drain them.
func (t *Tunnel) RunServer(ctx context.Context) error {
	ln, err := net.Listen("tcp", t.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen server: %w", err)
	}

	t.log.Info("server listening", "listen", t.cfg.Listen)

	return t.acceptLoop(ctx, ln, func(conn net.Conn) {
		t.handleServer(ctx, conn)
	})
}

// handleServer runs the handshake and the smux session for one client connection. Once ctx
// is done the session refuses new streams and closes itself when its last stream ends, so
// in-flight transfers finish while an idle session goes away at once.
func (t *Tunnel) handleServer(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	if err := t.serverHandshake(conn, reader); err != nil {
		t.log.Debug("handshake rejected", "peer", conn.RemoteAddr().String(), "err", err)
		return
	}

	peer := conn.RemoteAddr()

	stream, err := xorstream.New(&bufConn{r: reader, Conn: conn}, t.cfg.Secret)
	if err != nil {
		t.log.Warn("session setup failed", "peer", peer.String(), "err", err)
		return
	}

	sess, err := smux.Server(stream, muxConfig())
	if err != nil {
		t.log.Warn("session setup failed", "peer", peer.String(), "err", err)
		return
	}
	defer sess.Close()

	// A forced drain cuts the session, and with it every stream still open on it.
	defer context.AfterFunc(t.hard, func() { sess.Close() })()

	// open and draining decide when the session may close: only once draining has started
	// and the last stream is gone, so in-flight transfers are never cut by a graceful drain.
	var (
		mu       sync.Mutex
		open     int
		draining bool
	)

	closeIfIdle := func() {
		mu.Lock()
		idle := draining && open == 0
		mu.Unlock()

		if idle {
			sess.Close()
		}
	}

	defer context.AfterFunc(ctx, func() {
		mu.Lock()
		draining = true
		mu.Unlock()

		closeIfIdle()
	})()

	// Ends in-flight dials when the session dies.
	sctx, cancel := context.WithCancel(t.hard)
	defer cancel()

	for {
		vs, err := sess.AcceptStream()
		if err != nil {
			return
		}

		// Keep accepting while draining so a new stream is refused at once instead of hanging
		// until the client times out.
		if ctx.Err() != nil {
			vs.Close()
			continue
		}

		mu.Lock()
		open++
		mu.Unlock()

		t.spawn(func() {
			defer func() {
				mu.Lock()
				open--
				mu.Unlock()

				closeIfIdle()
			}()
			defer vs.Close()

			t.handleStream(sctx, peer, vs)
		})
	}
}

// handleStream detects the proxy protocol from the first byte of a tunnel stream (SOCKS5,
// SOCKS4/4a or HTTP), dials the requested target through the ACL and relays both ways. The
// request must complete within defaultTimeout. The client never parses any of this: it only
// pipes bytes, so the detection lives here and the wire format stays protocol-agnostic.
func (t *Tunnel) handleStream(ctx context.Context, peer net.Addr, vs *smux.Stream) {
	if err := vs.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return
	}

	lr := &io.LimitedReader{R: vs, N: maxHeader}
	br := bufio.NewReader(lr)

	first, err := br.Peek(1)
	if err != nil {
		t.log.Debug("proxy request rejected", "peer", peer.String(), "err", err)
		return
	}

	var (
		proto, target string
		req           *http.Request
	)

	switch b := first[0]; {
	case b == 0x05:
		proto = "socks5"

		// socks5 takes a ReadWriter. Read through br so the peeked byte is not lost.
		target, err = socks5.ReadRequest(struct {
			io.Reader
			io.Writer
		}{br, vs})
	case b == 0x04:
		proto = "socks4"
		if target, err = socks4.ReadRequest(br); err != nil {
			_ = socks4.WriteReply(vs, false)
		}
	case b >= 'A' && b <= 'Z':
		proto = "http"
		if req, target, err = httpproxy.ReadRequest(br); err != nil {
			_ = httpproxy.WriteStatus(vs, http.StatusBadRequest)
		} else if req.Method == http.MethodConnect {
			proto = "http-connect"
		}
	default:
		err = fmt.Errorf("unknown protocol byte 0x%02x", b)
	}

	if err != nil {
		t.log.Debug("proxy request rejected", "peer", peer.String(), "proto", proto, "err", err)
		return
	}

	// The header cap guards request parsing only. It must not truncate the relayed body.
	lr.N = math.MaxInt64

	d := net.Dialer{Timeout: defaultTimeout, Control: t.dialControl}
	dest, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		t.log.Warn("target unreachable", "target", target, "proto", proto, "err", err)
		t.replyFailure(vs, proto, err)
		return
	}
	defer dest.Close()

	if err := t.replySuccess(vs, proto, dest, req); err != nil {
		return
	}

	if err := vs.SetDeadline(time.Time{}); err != nil {
		return
	}

	t.log.Info("tunnel opened", "peer", peer.String(), "target", target, "proto", proto)
	relay.Pipe(&bufConn{r: br, Conn: vs}, dest)
}

// replySuccess tells the client the target is connected, in the client's own protocol.
// For plain HTTP there is no reply: the request is forwarded instead, and the deadline is
// cleared first so a slow body upload is not cut at defaultTimeout.
func (t *Tunnel) replySuccess(vs *smux.Stream, proto string, dest net.Conn, req *http.Request) error {
	switch proto {
	case "socks5":
		return socks5.WriteReply(vs, socks5.Succeeded, dest.LocalAddr())
	case "socks4":
		return socks4.WriteReply(vs, true)
	case "http-connect":
		return httpproxy.WriteStatus(vs, http.StatusOK)
	}

	if err := vs.SetDeadline(time.Time{}); err != nil {
		return err
	}

	return httpproxy.Forward(dest, req)
}

// replyFailure reports a failed dial in the client's own protocol. Reply errors are ignored
// because the stream is closed right after.
func (t *Tunnel) replyFailure(vs *smux.Stream, proto string, err error) {
	switch proto {
	case "socks5":
		_ = socks5.WriteReply(vs, replyCode(err), nil)
	case "socks4":
		_ = socks4.WriteReply(vs, false)
	default:
		_ = httpproxy.WriteStatus(vs, httpStatus(err))
	}
}

// serverHandshake plays the server side of the fake SMTP session and checks the EHLO line
// against the secret in constant time. Any failure closes the connection without a reply.
func (t *Tunnel) serverHandshake(conn net.Conn, r *bufio.Reader) error {
	if err := conn.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}

	if _, err := fmt.Fprintf(conn, "220 mail.google.com ESMTP\r\n"); err != nil {
		return fmt.Errorf("write 220: %w", err)
	}

	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read ehlo: %w", err)
	}

	want := "EHLO " + t.cfg.Secret
	if subtle.ConstantTimeCompare([]byte(strings.TrimRight(line, "\r\n")), []byte(want)) != 1 {
		return errors.New("invalid ehlo")
	}

	if _, err := fmt.Fprintf(conn, "250-OK\r\n250 STARTTLS\r\n"); err != nil {
		return fmt.Errorf("write 250: %w", err)
	}

	line, err = r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	if strings.TrimRight(line, "\r\n") != "DATA" {
		return errors.New("invalid data command")
	}

	if _, err := fmt.Fprintf(conn, "354 Go ahead\r\n"); err != nil {
		return fmt.Errorf("write 354: %w", err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear deadline: %w", err)
	}

	return nil
}

// dialControl refuses blocked target addresses unless AllowPrivate is set. It runs after DNS
// resolution, so it also stops names that resolve to blocked ranges.
func (t *Tunnel) dialControl(_, address string, _ syscall.RawConn) error {
	if t.cfg.AllowPrivate {
		return nil
	}

	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address: %w", err)
	}

	if isBlocked(ap.Addr()) {
		return errBlocked
	}

	return nil
}

// isBlocked reports whether a is loopback, private, link-local, multicast or unspecified.
// It uses stdlib predicates only, so ranges such as CGNAT 100.64.0.0/10 are not covered.
func isBlocked(a netip.Addr) bool {
	a = a.Unmap()

	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

// replyCode maps a dial error to the closest SOCKS5 reply code.
func replyCode(err error) byte {
	var dnsErr *net.DNSError
	var netErr net.Error

	switch {
	case errors.Is(err, errBlocked):
		return socks5.NotAllowed
	case errors.Is(err, syscall.ECONNREFUSED):
		return socks5.ConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH):
		return socks5.NetworkUnreachable
	case errors.As(err, &dnsErr), errors.Is(err, syscall.EHOSTUNREACH):
		return socks5.HostUnreachable
	case errors.As(err, &netErr) && netErr.Timeout():
		return socks5.HostUnreachable
	default:
		return socks5.GeneralFailure
	}
}

// httpStatus maps a dial error to the status a proxy returns: 403 for the ACL, 504 for a
// timeout and 502 for everything else.
func httpStatus(err error) int {
	var netErr net.Error

	switch {
	case errors.Is(err, errBlocked):
		return http.StatusForbidden
	case errors.As(err, &netErr) && netErr.Timeout():
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}
