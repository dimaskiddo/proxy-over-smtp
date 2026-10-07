package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/pkg/relay"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/xorstream"
)

func (t *Tunnel) RunClient(ctx context.Context) error {
	ln, err := net.Listen("tcp", t.cfg.ClientListenAddr)
	if err != nil {
		return fmt.Errorf("listen client: %w", err)
	}

	t.log.Printf("Client Listening on %s -> Tunnel to %s", t.cfg.ClientListenAddr, t.cfg.ClientRemoteAddr)

	err = t.acceptLoop(ctx, ln, func(local net.Conn) {
		t.handleClient(ctx, local)
	})

	t.log.Println("Shutting Down Client Listener...")
	t.closeSession()

	return err
}

func (t *Tunnel) closeSession() {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()

	if t.sess != nil {
		t.sess.Close()
		t.sess = nil
	}
}

func (t *Tunnel) handleClient(ctx context.Context, local net.Conn) {
	remoteStream, err := t.openStream(ctx)
	if err != nil {
		t.log.Printf("Open Stream Failed: %v", err)
		return
	}
	defer remoteStream.Close()

	relay.Pipe(local, remoteStream)
}

// openStream retries once on a fresh session: a peer restart leaves a dead session that
// IsClosed reports as open until keepalive times out.
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

// getSession holds sessMu while dialing so concurrent local connections share one dial.
func (t *Tunnel) getSession(ctx context.Context) (*smux.Session, error) {
	t.sessMu.Lock()
	defer t.sessMu.Unlock()

	if t.sess != nil && !t.sess.IsClosed() {
		return t.sess, nil
	}

	d := net.Dialer{Timeout: defaultTimeout}
	remote, err := d.DialContext(ctx, "tcp", t.cfg.ClientRemoteAddr)
	if err != nil {
		return nil, fmt.Errorf("dial server: %w", err)
	}

	reader := bufio.NewReader(remote)
	if err := t.clientHandshake(remote, reader); err != nil {
		remote.Close()
		return nil, fmt.Errorf("smtp handshake: %w", err)
	}

	stream, err := xorstream.New(&bufConn{r: reader, Conn: remote}, t.cfg.AuthSecret)
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

	if _, err := fmt.Fprintf(conn, "EHLO %s\r\n", t.cfg.AuthSecret); err != nil {
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
