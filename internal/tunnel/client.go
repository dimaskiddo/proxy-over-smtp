package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/pkg/relay"
)

// RunClient listens for local proxy connections and pipes each one into a stream on the
// shared tunnel session until ctx is done. It then stops accepting and returns: open
// transfers keep running until Shutdown drains them and closes the session.
func (t *Tunnel) RunClient(ctx context.Context) error {
	ln, err := t.listen(ctx)
	if err != nil {
		return fmt.Errorf("listen client: %w", err)
	}

	t.log.Info("client listening", "listen", t.cfg.Listen, "remote", t.cfg.Remote, "tls", t.tlsCfg != nil)

	return t.acceptLoop(ctx, ln, func(local net.Conn) {
		t.handleClient(t.hard, local)
	})
}

// closeSession closes every pooled session, if any, and stops the pool from dialing more.
// Streams on them end with an error.
func (t *Tunnel) closeSession() {
	t.poolMu.Lock()

	var sessions []*smux.Session

	for _, s := range t.slots {
		if s.sess != nil {
			sessions = append(sessions, s.sess)
		}
	}

	t.slots = nil
	t.closed = true
	t.poolMu.Unlock()

	for _, sess := range sessions {
		sess.Close()
	}
}

// handleClient pipes one local connection into a new tunnel stream. The first byte decides
// whether the application speaks TLS to the proxy (0x16, a ClientHello): that is terminated
// here when a certificate is configured, and everything else passes through untouched. The
// peek runs before the stream opens so idle or scanning connections cost the server nothing.
func (t *Tunnel) handleClient(ctx context.Context, local net.Conn) {
	br := bufio.NewReader(local)

	if err := local.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return
	}

	first, err := br.Peek(1)
	if err != nil {
		return
	}

	bc := &bufConn{r: br, Conn: local}

	var app io.ReadWriteCloser = bc

	if first[0] == 0x16 {
		if t.tlsCfg == nil {
			t.log.Debug("tls not enabled", "peer", local.RemoteAddr().String())
			return
		}

		// The handshake runs under the deadline set above and replays the peeked ClientHello bytes.
		tc := tls.Server(bc, t.tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			t.log.Debug("tls handshake failed", "peer", local.RemoteAddr().String(), "err", err)
			return
		}

		app = tc
	}

	if err := local.SetDeadline(time.Time{}); err != nil {
		return
	}

	remoteStream, err := t.openStream(ctx)
	if err != nil {
		t.log.Warn("open stream failed", "peer", local.RemoteAddr().String(), "err", err)
		return
	}
	defer remoteStream.Close()

	// Only the local peer is logged, never the target: the client does not parse the request,
	// so the destination is not known here, and it stays the server's audit field.
	peer := local.RemoteAddr().String()
	start := time.Now()

	up, down := relay.PipeCount(app, remoteStream)

	t.log.Info("connection closed",
		"peer", peer, "up", megabytes(up), "down", megabytes(down),
		"dur", time.Since(start).Round(time.Millisecond).String())
}

// megabytes renders a byte count for the client's audit line. The unit is 10^6 bytes rather than
// 2^20, because the label says MB and the number has to mean the same thing. Three decimals keep a
// transfer smaller than a megabyte visible instead of rounding it to zero, and the fixed width keeps
// one column readable down a page of events.
func megabytes(n int64) string {
	return strconv.FormatFloat(float64(n)/1e6, 'f', 3, 64) + " MB"
}

// dialSession dials the server, runs the handshake and wraps the connection in a new session.
func (t *Tunnel) dialSession(ctx context.Context) (*smux.Session, error) {
	d := dialer(nil)

	remote, err := d.DialContext(ctx, "tcp", t.cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	reader := bufio.NewReader(remote)

	stop := context.AfterFunc(ctx, func() { remote.Close() })
	nonce, err := t.clientHandshake(remote, reader)

	// A false stop means ctx fired mid-handshake and already closed remote, so any I/O error
	// is a side effect: report the cancellation instead.
	if !stop() {
		remote.Close()
		return nil, fmt.Errorf("smtp handshake: %w", ctx.Err())
	}

	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("smtp handshake: %w", err)
	}

	stream, err := t.wrapStream(&bufConn{r: reader, Conn: remote}, nonce, false)
	if err != nil {
		remote.Close()
		return nil, err
	}

	sess, err := smux.Client(stream, muxConfig())
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("smux client: %w", err)
	}

	return sess, nil
}

// clientHandshake plays the client side of the fake SMTP session. It reads the server nonce from
// the greeting, sends a conforming envelope with the HMAC proof as the X-PROOF parameter on MAIL
// FROM, and returns the nonce for key derivation. Replies are checked by prefix only: the server
// is the side that authenticates.
func (t *Tunnel) clientHandshake(conn net.Conn, r *bufio.Reader) ([]byte, error) {
	if err := conn.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	line, err := readLine(r, maxLine)
	if err != nil {
		return nil, fmt.Errorf("read 220: %w", err)
	}

	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "220" {
		return nil, fmt.Errorf("unexpected greeting")
	}

	nonce, err := base64.StdEncoding.DecodeString(fields[len(fields)-1])
	if err != nil || len(nonce) != nonceLen {
		return nil, fmt.Errorf("bad server nonce")
	}

	if _, err := fmt.Fprintf(conn, "EHLO %s\r\n", ehloHost); err != nil {
		return nil, fmt.Errorf("write ehlo: %w", err)
	}

	if err := readReply(r); err != nil {
		return nil, err
	}

	if _, err := fmt.Fprintf(conn, "%s\r\n", mailCommand(t.cfg.Secret, nonce)); err != nil {
		return nil, fmt.Errorf("write mail: %w", err)
	}

	if err := readReply(r); err != nil {
		return nil, err
	}

	if _, err := fmt.Fprintf(conn, "RCPT TO:<%s>\r\n", rcptTo); err != nil {
		return nil, fmt.Errorf("write rcpt: %w", err)
	}

	if err := readReply(r); err != nil {
		return nil, err
	}

	if _, err := fmt.Fprintf(conn, "DATA\r\n"); err != nil {
		return nil, fmt.Errorf("write data: %w", err)
	}

	line, err = readLine(r, maxLine)
	if err != nil {
		return nil, fmt.Errorf("read 354: %w", err)
	}

	if !strings.HasPrefix(line, "354") {
		return nil, fmt.Errorf("unexpected data reply")
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear deadline: %w", err)
	}

	return nonce, nil
}

// readReply reads one 250 reply. Every step from EHLO onwards takes the same answer, but EHLO may
// send continuations, so it runs to the first final line. The cap keeps a peer from holding the
// handshake open with continuations forever, and any other code fails at once rather than waiting
// for a close.
func readReply(r *bufio.Reader) error {
	for i := 0; i < maxReplyLines; i++ {
		line, err := readLine(r, maxLine)
		if err != nil {
			return fmt.Errorf("read 250: %w", err)
		}

		switch {
		case strings.HasPrefix(line, "250 "):
			return nil
		case !strings.HasPrefix(line, "250-"):
			return fmt.Errorf("unexpected reply")
		}
	}

	return fmt.Errorf("too many 250 replies")
}
