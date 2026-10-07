package tunnel

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/pkg/relay"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/socks5"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/xorstream"
)

var errBlocked = errors.New("target address not allowed")

func (t *Tunnel) RunServer(ctx context.Context) error {
	ln, err := net.Listen("tcp", t.cfg.ServerListenAddr)
	if err != nil {
		return fmt.Errorf("listen server: %w", err)
	}

	t.log.Printf("Server Listening on %s", t.cfg.ServerListenAddr)

	err = t.acceptLoop(ctx, ln, func(conn net.Conn) {
		t.handleServer(ctx, conn)
	})

	t.log.Println("Shutting Down Server Listener...")
	return err
}

func (t *Tunnel) handleServer(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	if err := t.serverHandshake(conn, reader); err != nil {
		return
	}

	stream, err := xorstream.New(&bufConn{r: reader, Conn: conn}, t.cfg.AuthSecret)
	if err != nil {
		return
	}

	sess, err := smux.Server(stream, muxConfig())
	if err != nil {
		return
	}
	defer sess.Close()

	for {
		vs, err := sess.AcceptStream()
		if err != nil {
			return
		}

		t.conns.Go(func() {
			defer vs.Close()
			t.handleStream(ctx, conn.RemoteAddr(), vs)
		})
	}
}

func (t *Tunnel) handleStream(ctx context.Context, peer net.Addr, vs *smux.Stream) {
	if err := vs.SetDeadline(time.Now().Add(defaultTimeout)); err != nil {
		return
	}

	target, err := socks5.ReadRequest(vs)
	if err != nil {
		return
	}

	d := net.Dialer{Timeout: defaultTimeout, Control: t.dialControl}
	dest, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		t.log.Printf("Failed to Reach %s: %v", target, err)
		_ = socks5.WriteReply(vs, replyCode(err), nil)
		return
	}
	defer dest.Close()

	if err := socks5.WriteReply(vs, socks5.Succeeded, dest.LocalAddr()); err != nil {
		return
	}

	if err := vs.SetDeadline(time.Time{}); err != nil {
		return
	}

	t.log.Printf("Tunnel: %s -> %s", peer, target)
	relay.Pipe(vs, dest)
}

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

	want := "EHLO " + t.cfg.AuthSecret
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

// dialControl runs after DNS resolution, so it also stops names that resolve to blocked ranges.
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

// isBlocked uses stdlib predicates only: ranges such as CGNAT 100.64.0.0/10 are not covered.
func isBlocked(a netip.Addr) bool {
	a = a.Unmap()

	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

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
