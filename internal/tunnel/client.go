package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/pkg/relay"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/xorstream"
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

// closeSession closes the shared session, if any. Streams on it end with an error.
func (t *Tunnel) closeSession() {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()

	if t.sess != nil {
		t.sess.Close()
		t.sess = nil
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
		t.log.Warn("open stream failed", "err", err)
		return
	}
	defer remoteStream.Close()

	relay.Pipe(app, remoteStream)
}

// openStream opens a stream on the shared session, creating the session if needed. It retries
// once on a fresh session: a peer restart leaves a dead session that IsClosed reports as open
// until keepalive times out.
func (t *Tunnel) openStream(ctx context.Context) (*smux.Stream, error) {
	var err error
	for range 2 {
		var sess *smux.Session
		if sess, err = t.getSession(ctx); err != nil {
			return nil, err
		}

		var s *smux.Stream
		if s, err = sess.OpenStream(); err == nil {
			return s, nil
		}

		sess.Close()
	}

	return nil, err
}

// getSession returns the open shared session, dialing the server and running the handshake
// when there is none. It holds sessMu while dialing so concurrent local connections share
// one dial instead of each opening a TCP connection.
func (t *Tunnel) getSession(ctx context.Context) (*smux.Session, error) {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()

	if t.sess != nil && !t.sess.IsClosed() {
		return t.sess, nil
	}

	d := dialer(nil)
	remote, err := d.DialContext(ctx, "tcp", t.cfg.Remote)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	reader := bufio.NewReader(remote)

	stop := context.AfterFunc(ctx, func() { remote.Close() })
	err = t.clientHandshake(remote, reader)

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

	stream, err := xorstream.New(&bufConn{r: reader, Conn: remote}, t.cfg.Secret)
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("xor stream: %w", err)
	}

	sess, err := smux.Client(stream, muxConfig())
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("smux client: %w", err)
	}

	t.sess = sess
	return t.sess, nil
}

// clientHandshake plays the client side of the fake SMTP session. The greeting and replies
// are checked by prefix only; the server is the side that authenticates.
func (t *Tunnel) clientHandshake(conn net.Conn, r *bufio.Reader) error {
	if err := conn.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}

	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read 220: %w", err)
	}

	if !strings.HasPrefix(line, "220") {
		return fmt.Errorf("unexpected greeting")
	}

	if _, err := fmt.Fprintf(conn, "EHLO %s\r\n", t.cfg.Secret); err != nil {
		return fmt.Errorf("write ehlo: %w", err)
	}

	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read 250: %w", err)
		}

		if strings.HasPrefix(line, "250 ") {
			break
		}
	}

	if _, err := fmt.Fprintf(conn, "DATA\r\n"); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	line, err = r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read 354: %w", err)
	}

	if !strings.HasPrefix(line, "354") {
		return fmt.Errorf("unexpected data reply")
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear deadline: %w", err)
	}

	return nil
}
