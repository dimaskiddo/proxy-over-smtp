package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
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
			srv, err := New(config.Config{Secret: "s3cret", Cipher: config.CipherAES}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}

			cli, err := New(config.Config{Secret: tt.clientSecret, Cipher: config.CipherAES}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}

			c, s := net.Pipe()
			defer c.Close()
			defer s.Close()

			srvErr := make(chan error, 1)
			go func() {
				_, err := srv.serverHandshake(s, bufio.NewReader(s))
				if err != nil {
					s.Close()
				}
				srvErr <- err
			}()

			_, cliErr := cli.clientHandshake(c, bufio.NewReader(c))
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
		{"0.1.2.3", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"192.0.0.1", true},
		{"240.0.0.1", true},
		{"255.255.255.255", true},
		{"::ffff:127.0.0.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"192.0.1.1", false},
		{"239.255.255.255", true},
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
func newPair(t testing.TB, cliCfg config.Config) *pair {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	// The two ends must agree on the cipher, so the client's choice drives the server's.
	if cliCfg.Cipher == "" {
		cliCfg.Cipher = config.CipherAES
	}

	srv, err := New(config.Config{Secret: "s3cret", Cipher: cliCfg.Cipher, AllowPrivate: true}, log)
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
func socksConnect(t testing.TB, cli *Tunnel, target net.Addr) io.ReadWriteCloser {
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
func echoListener(t testing.TB) net.Listener {
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

// syncBuffer is a bytes.Buffer safe to read while handler goroutines write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the lock.
func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.Write(p)
}

// String returns everything written so far.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.String()
}

// TestClientConnectionLog checks the client audits each proxied connection with the local peer
// only: the target is not parsed on this side, so it must never appear in a client line.
func TestClientConnectionLog(t *testing.T) {
	p := newPair(t, config.Config{})

	var out syncBuffer
	p.cli.log = slog.New(slog.NewTextHandler(&out, nil))

	echo := echoListener(t)
	echoAddr := echo.Addr().String()

	// Through the local proxy port, not a bare stream: the log line is emitted by handleClient.
	c := dialClient(t, p)
	roundTrip(t, socksConnectOn(t, c, echo.Addr()))
	c.Close()

	deadline := time.Now().Add(5 * time.Second)

	var line string
	for time.Now().Before(deadline) {
		if line = out.String(); strings.Contains(line, "connection closed") {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(line, "connection closed") {
		t.Fatalf("no client connection line in %q", line)
	}

	if !strings.Contains(line, "peer=127.0.0.1:") {
		t.Fatalf("client line missing the local peer in %q", line)
	}

	if !strings.Contains(line, "up=") || !strings.Contains(line, "down=") {
		t.Fatalf("client line missing transfer counts in %q", line)
	}

	if strings.Contains(line, "target=") || strings.Contains(line, echoAddr) {
		t.Fatalf("client line leaks the target in %q", line)
	}
}

func TestTunnelStreams(t *testing.T) {
	for _, cipher := range []string{config.CipherAES, config.CipherXOR} {
		t.Run(cipher, func(t *testing.T) {
			cli := newPair(t, config.Config{Cipher: cipher}).cli
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
		})
	}
}

// BenchmarkProxyThroughput pumps bytes through a real client and server pair over loopback and
// drains them back through an echo target. stream counts concurrent proxied connections, which is
// how a link above one stream's window/RTT ceiling is meant to be driven. The pooled case keeps
// four streams per session, so it spans two sessions: that is the aggregate shape a multi-gigabit
// link needs. Numbers are informational, not a gate.
func BenchmarkProxyThroughput(b *testing.B) {
	cases := []struct {
		name    string
		cfg     config.Config
		streams int
	}{
		{"single", config.Config{}, 1},
		{"multi", config.Config{}, 8},
		{"pooled", config.Config{PoolMin: 1, PoolMax: 4, MaxStreams: 4}, 8},
	}

	for _, cipher := range []string{config.CipherAES, config.CipherXOR} {
		for _, tc := range cases {
			b.Run(fmt.Sprintf("%s/%s/%dstreams", cipher, tc.name, tc.streams), func(b *testing.B) {
				cfg := tc.cfg
				cfg.Cipher = cipher

				cli := newPair(b, cfg).cli
				echo := echoListener(b)

				// Sized so one round is one relay buffer; each stream reads its own echo back, so a
				// paused reader cannot stall the session.
				payload := make([]byte, 128*1024)
				total := int64(len(payload)) * int64(tc.streams)

				conns := make([]io.ReadWriteCloser, tc.streams)
				for i := range conns {
					conns[i] = socksConnect(b, cli, echo.Addr())
				}

				b.Cleanup(func() {
					for _, c := range conns {
						c.Close()
					}
				})

				b.SetBytes(total)
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					var wg sync.WaitGroup

					for _, c := range conns {
						wg.Add(1)

						go func(c io.ReadWriteCloser) {
							defer wg.Done()

							if _, err := c.Write(payload); err != nil {
								b.Error(err)
								return
							}

							if _, err := io.ReadFull(c, payload); err != nil {
								b.Error(err)
							}
						}(c)
					}

					wg.Wait()
				}
			})
		}
	}
}

// TestShutdownBeforeRun checks the documented ordering is now enforced in code: a Tunnel that
// was shut down before any Run never serves, and never panics.
func TestShutdownBeforeRun(t *testing.T) {
	srv, err := New(config.Config{Secret: "s3cret", Cipher: config.CipherAES}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown before run: %v", err)
	}

	// Idempotent: a second Shutdown is a no-op, not a panic.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if err := srv.acceptLoop(context.Background(), ln, func(net.Conn) {}); err == nil {
		t.Fatal("acceptLoop after shutdown returned nil, want error")
	}

	if err := srv.RunServer(context.Background()); err == nil {
		t.Fatal("RunServer after shutdown returned nil, want error")
	}
}

// TestShutdownBeforeRunClient is the client-side counterpart: a shut-down client refuses to run.
func TestShutdownBeforeRunClient(t *testing.T) {
	cli, err := New(config.Config{Secret: "s3cret", Cipher: config.CipherAES, Remote: "127.0.0.1:465"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if err := cli.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown before run: %v", err)
	}

	if err := cli.RunClient(context.Background()); err == nil {
		t.Fatal("RunClient after shutdown returned nil, want error")
	}
}

// TestSpawnDroppedAfterShutdown checks a handler raced in after Shutdown is dropped instead of
// tripping the WaitGroup misuse panic and instead of running unwaited.
func TestSpawnDroppedAfterShutdown(t *testing.T) {
	srv, err := New(config.Config{Secret: "s3cret", Cipher: config.CipherAES}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	ran := make(chan struct{})
	if srv.spawn(func() { close(ran) }) {
		t.Fatal("spawn after shutdown reported true, want false")
	}

	if srv.Active() != 0 {
		t.Fatalf("Active = %d after dropped spawn, want 0", srv.Active())
	}

	select {
	case <-ran:
		t.Fatal("spawned handler ran after shutdown")
	default:
	}
}

// TestMaxStreamsRefused checks the per-session cap: streams beyond MaxStreams are closed at
// once instead of hanging until the client deadline.
func TestMaxStreamsRefused(t *testing.T) {
	p := newPair(t, config.Config{})
	p.srv.cfg.MaxStreams = 2

	echo := echoListener(t)

	held := make([]io.ReadWriteCloser, 0, 2)
	for i := 0; i < 2; i++ {
		s := socksConnect(t, p.cli, echo.Addr())
		defer s.Close()
		held = append(held, s)
	}

	// The server opens the third stream and closes it in the same accept iteration, so by the
	// time the request reaches the relay the stream is already gone.
	s, err := p.cli.openStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := io.ReadFull(s, make([]byte, 2)); err == nil {
		t.Fatal("third stream succeeded past MaxStreams, want refusal")
	}
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
