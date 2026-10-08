package tunnel

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
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
)

// errBlocked marks a dial refused by the target ACL so replies can map it to "not allowed".
var errBlocked = errors.New("target address not allowed")

// maxHeader caps the bytes read while parsing a proxy request.
const maxHeader = 64 << 10

// RunServer listens on the configured address and serves tunnel clients until ctx is done.
// It then stops accepting and returns without touching open connections: call Shutdown to
// drain them.
func (t *Tunnel) RunServer(ctx context.Context) error {
	ln, err := t.listen(ctx)
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

	nonce, err := t.serverHandshake(conn, reader)
	if err != nil {
		t.log.Debug("handshake rejected", "peer", conn.RemoteAddr().String(), "err", err)
		return
	}

	peer := conn.RemoteAddr()

	stream, err := t.wrapStream(&bufConn{r: reader, Conn: conn}, nonce, true)
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
		peak := open
		over := peak > t.cfg.MaxStreams
		mu.Unlock()

		// Refuse over the cap instead of queueing, so one peer cannot hold unbounded handlers.
		if over {
			mu.Lock()
			open--
			mu.Unlock()

			// peak is captured under the lock: the refused stream is off the count by now, and a
			// handler finishing meanwhile must not change what the refusal reports.
			t.log.Debug("stream refused", "peer", peer.String(), "open", peak, "max", t.cfg.MaxStreams)
			vs.Close()
			continue
		}

		if !t.spawn(func() {
			defer func() {
				mu.Lock()
				open--
				mu.Unlock()

				closeIfIdle()
			}()
			defer vs.Close()

			t.handleStream(sctx, peer, vs)
		}) {
			// Shutdown started between the cap check and the spawn: give the stream back.
			mu.Lock()
			open--
			mu.Unlock()

			vs.Close()
			return
		}
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

	d := dialer(t.dialControl)
	dest, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		// target stays the last attribute so it lines up down the column on the audit stream.
		t.log.Warn("target unreachable", "proto", proto, "err", err, "target", target)
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

	t.log.Info("tunnel opened", "peer", peer.String(), "proto", proto, "target", target)
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

// readStage reads one command line and answers the commands that end the session at any stage:
// NOOP and RSET are acknowledged, VRFY, EXPN and HELP are refused as unimplemented, and QUIT is
// answered inside readCommand. It returns errUnsupported or errQuit once the reply is on the wire,
// so a stage only ever sees the lines it is meant to act on.
func readStage(w io.Writer, r *bufio.Reader, stage string) (string, error) {
	line, err := readCommand(w, r)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", stage, err)
	}

	reply, ok := probeReply(line)
	if !ok {
		return line, nil
	}

	if err := reject(w, reply, errUnsupported); err != nil {
		return "", fmt.Errorf("read %s: %w", stage, err)
	}

	return "", fmt.Errorf("read %s: %w", stage, errUnsupported)
}

// probeReply returns the reply that ends the session for a command a real server answers but does
// not act on, and whether line is one of them. NOOP and RSET change no transaction state, so 250 is
// the honest answer; VRFY, EXPN and HELP are refused as unimplemented rather than unrecognized,
// because a real server knows them and only declines to offer them. A real server keeps the session
// open after either; this one closes, so a probe gets one answer and nothing more.
func probeReply(line string) (string, bool) {
	switch {
	case isCommand(line, "NOOP"), isCommand(line, "RSET"):
		return replyOK, true
	case isCommand(line, "VRFY"), isCommand(line, "EXPN"), isCommand(line, "HELP"):
		return replyUnimplemented, true
	}

	return "", false
}

// serverHandshake plays the server side of the fake SMTP session. It sends a fresh nonce in the
// greeting, walks the client through an envelope a real server would accept, and checks the HMAC
// proof carried as the X-PROOF parameter on MAIL FROM in constant time, returning the nonce so the
// caller can derive the stream keys. Commands are case-insensitive (RFC 5321 §2.4) and HELO is
// taken as an EHLO alias, so the session reads as an ordinary relay to anything watching. A bad
// proof is answered with the same 500 as a malformed line, so no reply tells a probe which it sent;
// a command out of order gets 503, and an EHLO whose argument is missing gets 501. Any other
// argument is accepted, since it names the client and §4.1.4 forbids refusing mail over it. Only a
// failure with no reply to give closes without one.
func (t *Tunnel) serverHandshake(conn net.Conn, r *bufio.Reader) ([]byte, error) {
	if err := conn.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}

	if _, err := fmt.Fprintf(conn, "220 %s ESMTP %s\r\n", mailHost, base64.StdEncoding.EncodeToString(nonce)); err != nil {
		return nil, fmt.Errorf("write 220: %w", err)
	}

	line, err := readStage(conn, r, "ehlo")
	if err != nil {
		return nil, err
	}

	// A transaction command before the greeting is out of order, not unrecognized.
	if isCommand(line, "MAIL") || isCommand(line, "RCPT") || isCommand(line, "DATA") {
		return nil, reject(conn, replySequence, errSequence)
	}

	// A verb this server does not know is the same 500 any unusable line gets. A verb it does know
	// with the argument missing is the parameter error a real relay answers a bare EHLO with, which
	// keeps the reply honest about the command without ever judging the name the client gives.
	if !isCommand(line, "EHLO") && !isCommand(line, "HELO") {
		return nil, reject(conn, replySyntax, errAuth)
	}

	if !ehloArgument(line) {
		return nil, reject(conn, replyParam, errAuth)
	}

	if _, err := fmt.Fprintf(conn, "250-%s\r\n250 %s\r\n", mailHost, extKeyword); err != nil {
		return nil, fmt.Errorf("write ehlo reply: %w", err)
	}

	line, err = readStage(conn, r, "mail")
	if err != nil {
		return nil, err
	}

	// A recipient or DATA before the sender is a sequence error, not a bad line, so the verb is
	// checked before the proof: what the reply says depends on the command, never on the secret.
	if isCommand(line, "RCPT") || isCommand(line, "DATA") {
		return nil, reject(conn, replySequence, errSequence)
	}

	if !hasPrefixFold(line, mailPrefix) {
		return nil, reject(conn, replySyntax, errAuth)
	}

	// The prefix is a public constant; only the value appended to it is secret, so only that
	// comparison is kept free of early returns.
	if subtle.ConstantTimeCompare([]byte(line[len(mailPrefix):]), []byte(proofValue(t.cfg.Secret, nonce))) != 1 {
		return nil, reject(conn, replySyntax, errAuth)
	}

	if _, err := io.WriteString(conn, replyOK+"\r\n"); err != nil {
		return nil, fmt.Errorf("write mail reply: %w", err)
	}

	line, err = readStage(conn, r, "rcpt")
	if err != nil {
		return nil, err
	}

	// A second sender would restart the transaction, which this session does not support; a real
	// server calls that a sequence error, and so does this one.
	if isCommand(line, "MAIL") || isCommand(line, "DATA") {
		return nil, reject(conn, replySequence, errSequence)
	}

	if !strings.EqualFold(line, rcptLine) {
		return nil, reject(conn, replySyntax, errAuth)
	}

	if _, err := io.WriteString(conn, replyOK+"\r\n"); err != nil {
		return nil, fmt.Errorf("write rcpt reply: %w", err)
	}

	line, err = readStage(conn, r, "data")
	if err != nil {
		return nil, err
	}

	if !isCommand(line, "DATA") {
		return nil, reject(conn, replySequence, errSequence)
	}

	if _, err := io.WriteString(conn, "354 End data with <CR><LF>.<CR><LF>\r\n"); err != nil {
		return nil, fmt.Errorf("write 354: %w", err)
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear deadline: %w", err)
	}

	return nonce, nil
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

// nonPublicPrefixes lists ranges the stdlib predicates do not cover but that are still not public
// unicast: CGNAT, the rest of 0.0.0.0/8 (often loopback-routed), IETF protocol assignments and
// the reserved block. Without them those targets would pass the ACL.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// isBlocked reports whether a is loopback, private, link-local, multicast or unspecified, or
// falls in a reserved prefix the stdlib predicates leave out.
func isBlocked(a netip.Addr) bool {
	a = a.Unmap()

	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}

	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return true
		}
	}

	return false
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
	case errors.As(err, &dnsErr), errors.Is(err, syscall.EHOSTUNREACH),
		errors.As(err, &netErr) && netErr.Timeout():
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
