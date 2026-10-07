//go:build unix

package tunnel

import (
	"context"
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

// TestSocketOptions checks that both ends of a connection carry the tuned options. Linux reports
// double the requested buffer, so buffers are asserted as a range, not an exact value.
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

	tests := []struct {
		name       string
		level, opt int
		min, max   int
	}{
		{"SO_RCVBUF", unix.SOL_SOCKET, unix.SO_RCVBUF, sockBuffer, 4 * sockBuffer},
		{"SO_SNDBUF", unix.SOL_SOCKET, unix.SO_SNDBUF, sockBuffer, 4 * sockBuffer},
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
