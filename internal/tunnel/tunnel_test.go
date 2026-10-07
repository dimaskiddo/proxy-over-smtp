package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

func TestHandshake(t *testing.T) {
	tests := []struct {
		name         string
		clientSecret string
		wantErr      bool
	}{
		{"match", "s3cret", false},
		{"wrong", "other", true},
		{"substring", "xx" + "s3cret" + "xx", true},
		{"prefix only", "s3cre", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := New(config.Config{Secret: "s3cret"}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}

			cli, err := New(config.Config{Secret: tt.clientSecret}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}

			c, s := net.Pipe()
			defer c.Close()
			defer s.Close()

			srvErr := make(chan error, 1)
			go func() {
				err := srv.serverHandshake(s, bufio.NewReader(s))
				if err != nil {
					s.Close()
				}
				srvErr <- err
			}()

			cliErr := cli.clientHandshake(c, bufio.NewReader(c))
			if (cliErr != nil) != tt.wantErr {
				t.Fatalf("client err = %v, wantErr %v", cliErr, tt.wantErr)
			}

			if err := <-srvErr; (err != nil) != tt.wantErr {
				t.Fatalf("server err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsBlocked(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"192.168.0.1", true},
		{"172.16.0.1", true},
		{"169.254.169.254", true},
		{"0.0.0.0", true},
		{"::ffff:127.0.0.1", true},
		{"8.8.8.8", false},
		{"2606:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := isBlocked(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Fatalf("isBlocked(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// pair is a client and a server Tunnel wired over loopback. stopServer and stopClient end
// the accept loops without draining, like a signal does.
type pair struct {
	cli, srv   *Tunnel
	cliAddr    string
	stopServer context.CancelFunc
	stopClient context.CancelFunc
	srvDone    chan struct{}
	cliDone    chan struct{}
}

// newPair runs a real server accept loop and a client accept loop over loopback. Cleanup stops
// both and drains them.
func newPair(t *testing.T, cliCfg config.Config) *pair {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	srv, err := New(config.Config{Secret: "s3cret", AllowPrivate: true}, log)
	if err != nil {
		t.Fatal(err)
	}

	sln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	cliCfg.Secret = "s3cret"
	cliCfg.Remote = sln.Addr().String()

	cli, err := New(cliCfg, log)
	if err != nil {
		t.Fatal(err)
	}

	cln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p := &pair{cli: cli, srv: srv, cliAddr: cln.Addr().String(), srvDone: make(chan struct{}), cliDone: make(chan struct{})}

	var sctx, cctx context.Context
	sctx, p.stopServer = context.WithCancel(context.Background())
	cctx, p.stopClient = context.WithCancel(context.Background())

	go func() {
		defer close(p.srvDone)
		_ = srv.acceptLoop(sctx, sln, func(c net.Conn) { srv.handleServer(sctx, c) })
	}()

	go func() {
		defer close(p.cliDone)
		_ = cli.acceptLoop(cctx, cln, func(c net.Conn) { cli.handleClient(cli.hard, c) })
	}()

	t.Cleanup(func() {
		p.stopServer()
		p.stopClient()
		<-p.srvDone
		<-p.cliDone

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = cli.Shutdown(ctx)
		_ = srv.Shutdown(ctx)
	})

	return p
}

// startPair returns the client Tunnel of a default pair.
func startPair(t *testing.T) *Tunnel {
	t.Helper()
	return newPair(t, config.Config{}).cli
}

// socksConnect opens a tunnel stream and runs the SOCKS5 CONNECT exchange to target.
func socksConnect(t *testing.T, cli *Tunnel, target net.Addr) io.ReadWriteCloser {
	t.Helper()

	s, err := cli.openStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ap := target.(*net.TCPAddr).AddrPort()
	req := []byte{5, 1, 0, 1}
	req = append(req, ap.Addr().AsSlice()...)
	req = binary.BigEndian.AppendUint16(req, ap.Port())

	if _, err := s.Write(append([]byte{5, 1, 0}, req...)); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 2+10)
	if _, err := io.ReadFull(s, reply); err != nil {
		t.Fatal(err)
	}

	if reply[0] != 5 || reply[1] != 0 || reply[2] != 5 || reply[3] != 0 {
		t.Fatalf("bad socks reply % x", reply)
	}

	return s
}

// echoListener returns a loopback listener that echoes every connection until the test ends.
func echoListener(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	return ln
}

func TestTunnelStreams(t *testing.T) {
	cli := startPair(t)
	echo := echoListener(t)

	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			s := socksConnect(t, cli, echo.Addr())
			defer s.Close()

			go s.Write(payload)

			got := make([]byte, len(payload))
			if _, err := io.ReadFull(s, got); err != nil {
				t.Error(err)
				return
			}

			if !bytes.Equal(got, payload) {
				t.Error("payload mismatch")
			}
		}()
	}

	wg.Wait()
}

// TestStreamIsolation checks that a stream nobody reads does not block other streams in the
// session.
func TestStreamIsolation(t *testing.T) {
	cli := startPair(t)
	echo := echoListener(t)

	flood, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer flood.Close()

	go func() {
		c, err := flood.Accept()
		if err != nil {
			return
		}
		defer c.Close()

		_, _ = c.Write(make([]byte, 32<<20))
	}()

	stalled := socksConnect(t, cli, flood.Addr())
	defer stalled.Close()

	time.Sleep(200 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		s := socksConnect(t, cli, echo.Addr())
		defer s.Close()

		msg := []byte("still alive")
		if _, err := s.Write(msg); err != nil {
			done <- err
			return
		}

		got := make([]byte, len(msg))
		_, err := io.ReadFull(s, got)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("echo stream blocked by stalled stream")
	}
}
