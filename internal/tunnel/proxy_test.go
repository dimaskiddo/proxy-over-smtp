package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

// dialClient connects to the client's local proxy port like an application would.
func dialClient(t *testing.T, p *pair) net.Conn {
	t.Helper()

	c, err := net.Dial("tcp", p.cliAddr)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	return c
}

func socks4Request(addr net.Addr) []byte {
	ap := addr.(*net.TCPAddr).AddrPort()
	req := []byte{4, 1}
	req = binary.BigEndian.AppendUint16(req, ap.Port())
	req = append(req, ap.Addr().AsSlice()...)

	return append(req, 0)
}

func socks4aRequest(host string, port uint16) []byte {
	req := []byte{4, 1}
	req = binary.BigEndian.AppendUint16(req, port)
	req = append(req, 0, 0, 0, 1, 0)
	req = append(req, host...)

	return append(req, 0)
}

func TestProtocols(t *testing.T) {
	p := newPair(t, config.Config{})
	echo := echoListener(t)
	echoPort := uint16(echo.Addr().(*net.TCPAddr).Port)

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s proxyconn=%q", r.URL.Path, r.Header.Get("Proxy-Connection"))
	}))
	defer web.Close()

	tests := []struct {
		name string
		run  func(t *testing.T, c net.Conn)
	}{
		{"socks5", func(t *testing.T, c net.Conn) {
			s := socksConnect(t, p.cli, echo.Addr())
			defer s.Close()
			roundTrip(t, s)
		}},
		{"socks4", func(t *testing.T, c net.Conn) {
			c.Write(socks4Request(echo.Addr()))
			reply := make([]byte, 8)
			if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0x5A {
				t.Fatalf("reply % x, err %v", reply, err)
			}
			roundTrip(t, c)
		}},
		{"socks4a", func(t *testing.T, c net.Conn) {
			c.Write(socks4aRequest("localhost", echoPort))
			reply := make([]byte, 8)
			if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0x5A {
				t.Fatalf("reply % x, err %v", reply, err)
			}
			roundTrip(t, c)
		}},
		{"socks4 refused", func(t *testing.T, c net.Conn) {
			c.Write(socks4Request(closedAddr(t)))
			reply := make([]byte, 8)
			if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0x5B {
				t.Fatalf("reply % x, err %v", reply, err)
			}
		}},
		{"http connect", func(t *testing.T, c net.Conn) {
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %[1]s\r\n\r\n", echo.Addr())
			br := bufio.NewReader(c)
			resp, err := http.ReadResponse(br, nil)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("resp %v, err %v", resp, err)
			}
			roundTrip(t, &bufConn{r: br, Conn: c})
		}},
		{"http connect refused", func(t *testing.T, c net.Conn) {
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: x\r\n\r\n", closedAddr(t))
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil || resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("resp %v, err %v", resp, err)
			}
		}},
		{"http plain", func(t *testing.T, c net.Conn) {
			fmt.Fprintf(c, "GET %s/hello HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\n\r\n", web.URL, web.Listener.Addr())
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 || string(body) != `path=/hello proxyconn=""` {
				t.Fatalf("status %d body %q", resp.StatusCode, body)
			}
		}},
		{"http origin form", func(t *testing.T, c net.Conn) {
			fmt.Fprint(c, "GET /x HTTP/1.1\r\nHost: x\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil || resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("resp %v, err %v", resp, err)
			}
		}},
		{"unknown protocol", func(t *testing.T, c net.Conn) {
			c.Write([]byte{0x01, 0x02, 0x03})
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("want closed connection")
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, dialClient(t, p))
		})
	}
}

func TestBlockedTarget(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	srv, err := New(config.Config{Secret: "s3cret"}, log)
	if err != nil {
		t.Fatal(err)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.acceptLoop(ctx, ln, func(c net.Conn) { srv.handleServer(ctx, c) })
	}()
	defer func() {
		cancel()
		<-done
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}()

	cli, err := New(config.Config{Secret: "s3cret", Remote: ln.Addr().String()}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.closeSession()

	s, err := cli.openStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fmt.Fprint(s, "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(s), nil)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resp %v, err %v", resp, err)
	}
}

func TestTLSListener(t *testing.T) {
	certFile, keyFile, pool := selfSigned(t)
	echo := echoListener(t)

	p := newPair(t, config.Config{TLSCert: certFile, TLSKey: keyFile})

	raw := dialClient(t, p)
	c := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: "localhost"})

	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: x\r\n\r\n", echo.Addr())
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp %v, err %v", resp, err)
	}
	roundTrip(t, &bufConn{r: br, Conn: c})

	plain := dialClient(t, p)
	s := socksConnectOn(t, plain, echo.Addr())
	roundTrip(t, s)
}

func TestTLSNotEnabled(t *testing.T) {
	p := newPair(t, config.Config{})
	c := dialClient(t, p)

	c.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 1, 2, 3, 4, 5})
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("want closed connection")
	}
}

func TestGracefulDrain(t *testing.T) {
	p := newPair(t, config.Config{})
	echo := echoListener(t)

	open := socksConnect(t, p.cli, echo.Addr())
	defer open.Close()

	p.stopServer()
	<-p.srvDone

	if _, err := open.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 4)
	if _, err := io.ReadFull(open, got); err != nil || string(got) != "ping" {
		t.Fatalf("existing stream cut during drain: %q, %v", got, err)
	}

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- p.srv.Shutdown(ctx)
	}()

	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v with an open stream", err)
	case <-time.After(200 * time.Millisecond):
	}

	if p.srv.Active() == 0 {
		t.Fatal("Active() = 0 with an open stream")
	}

	open.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil after clean drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the stream closed")
	}
}

func TestForcedDrain(t *testing.T) {
	p := newPair(t, config.Config{})
	echo := echoListener(t)

	open := socksConnect(t, p.cli, echo.Addr()).(net.Conn)
	defer open.Close()

	p.stopServer()
	<-p.srvDone

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := p.srv.Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatalf("Shutdown = %v, want DeadlineExceeded", err)
	}

	open.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := open.Read(make([]byte, 1)); err == nil {
		t.Fatal("stream still open after forced drain")
	}
}

func TestIdleSessionClosesOnDrain(t *testing.T) {
	p := newPair(t, config.Config{})
	echo := echoListener(t)

	s := socksConnect(t, p.cli, echo.Addr())
	s.Close()
	time.Sleep(100 * time.Millisecond)

	p.stopServer()
	<-p.srvDone

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v, idle session should drain at once", err)
	}
}

func roundTrip(t *testing.T, rw io.ReadWriter) {
	t.Helper()

	msg := []byte("hello through the tunnel")
	if _, err := rw.Write(msg); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(rw, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

// closedAddr returns a loopback address with nothing listening.
func closedAddr(t *testing.T) net.Addr {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr()
	ln.Close()

	return addr
}

// socksConnectOn runs a SOCKS5 CONNECT on an existing client connection.
func socksConnectOn(t *testing.T, c net.Conn, target net.Addr) io.ReadWriter {
	t.Helper()

	ap := target.(*net.TCPAddr).AddrPort()
	req := []byte{5, 1, 0, 5, 1, 0, 1}
	req = append(req, ap.Addr().AsSlice()...)
	req = binary.BigEndian.AppendUint16(req, ap.Port())

	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 12)
	if _, err := io.ReadFull(c, reply); err != nil || reply[3] != 0 {
		t.Fatalf("reply % x, err %v", reply, err)
	}

	return c
}

// selfSigned writes a throwaway localhost certificate and returns its files and a trust pool.
func selfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")

	write := func(path, typ string, b []byte) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(certFile, "CERTIFICATE", der)
	write(keyFile, "EC PRIVATE KEY", keyDER)

	pool = x509.NewCertPool()
	cert, _ := x509.ParseCertificate(der)
	pool.AddCert(cert)

	return certFile, keyFile, pool
}
