//go:build unix

package tunnel

import (
	"context"
	"io"
	"net"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

// sockInt reads one integer socket option from c.
func sockInt(t *testing.T, c net.Conn, level, opt int) int {
	t.Helper()

	rc, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}

	var (
		v    int
		gerr error
	)

	if err := rc.Control(func(fd uintptr) { v, gerr = unix.GetsockoptInt(int(fd), level, opt) }); err != nil {
		t.Fatal(err)
	}

	if gerr != nil {
		t.Fatal(gerr)
	}

	return v
}

// minSockBuffer is the floor a kernel-tuned send or receive buffer must clear. Every supported
// platform autotunes well past this, so a value below it means a fixed small buffer was set and
// throughput is capped near buffer/RTT.
const minSockBuffer = 64 * 1024

// TestSocketOptions checks that both ends of a connection carry the tuned options. Send and
// receive buffers must stay under kernel control: a small fixed buffer caps bulk throughput, so
// they are asserted to grow past minSockBuffer instead of matching an exact size.
func TestSocketOptions(t *testing.T) {
	tn := &Tunnel{cfg: config.Config{Listen: "127.0.0.1:0"}}

	ln, err := tn.listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}

		accepted <- c
	}()

	cli, err := dialer(nil).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	srv, ok := <-accepted
	if !ok {
		t.Fatal("accept failed")
	}
	defer srv.Close()

	// A burst gives kernel buffer autotuning a reason to grow both sockets.
	blob := make([]byte, 512*1024)
	go func() {
		if _, err := srv.Write(blob); err != nil {
			t.Error(err)
		}
	}()

	if _, err := io.CopyN(io.Discard, cli, int64(len(blob))); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		level, opt int
		min, max   int
	}{
		{"SO_RCVBUF", unix.SOL_SOCKET, unix.SO_RCVBUF, minSockBuffer, 1 << 30},
		{"SO_SNDBUF", unix.SOL_SOCKET, unix.SO_SNDBUF, minSockBuffer, 1 << 30},
		{"TCP_NODELAY", unix.IPPROTO_TCP, unix.TCP_NODELAY, 1, 1},
		{"SO_KEEPALIVE", unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1, 1},
	}

	for _, side := range []struct {
		name string
		conn net.Conn
	}{{"dialed", cli}, {"accepted", srv}} {
		for _, tc := range tests {
			t.Run(side.name+"/"+tc.name, func(t *testing.T) {
				if v := sockInt(t, side.conn, tc.level, tc.opt); v < tc.min || v > tc.max {
					t.Fatalf("%s = %d, want %d..%d", tc.name, v, tc.min, tc.max)
				}
			})
		}
	}
}

// TestListenReusePort checks that two listeners can share one port.
func TestListenReusePort(t *testing.T) {
	tn := &Tunnel{cfg: config.Config{Listen: "127.0.0.1:0"}}

	first, err := tn.listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	tn.cfg.Listen = first.Addr().String()

	second, err := tn.listen(context.Background())
	if err != nil {
		t.Fatalf("second listen on %s = %v, want reuseport to allow it", tn.cfg.Listen, err)
	}

	second.Close()
}
