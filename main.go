package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultTimeout = 30 * time.Second
	BufferSize     = 32 * 1024
)

var (
	auditLog   *log.Logger
	bufferPool = sync.Pool{
		New: func() interface{} { return make([]byte, BufferSize) },
	}
	activeConns sync.WaitGroup
)

var (
	Mode                                       string
	ClientListenAddr, ClientRemoteAddr         string
	ServerListenAddr, AuthSecret, AuditLogFile string
)

func init() {
	flag.StringVar(&Mode, "mode", "server", "Mode: 'client' or 'server'")
	flag.StringVar(&ClientListenAddr, "client", "0.0.0.0:1080", "Client listen address")
	flag.StringVar(&ClientRemoteAddr, "remote", "127.0.0.1:465", "Server remote address")
	flag.StringVar(&ServerListenAddr, "server", "0.0.0.0:465", "Server listen address")
	flag.StringVar(&AuthSecret, "secret", "THIS_IS_YOUR_SECRET_WORD", "Authentication secret")
	flag.StringVar(&AuditLogFile, "log-file", "./proxy-over-smtp.log", "Log file path")
	flag.Parse()

	f, _ := os.OpenFile(AuditLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	auditLog = log.New(io.MultiWriter(os.Stdout, f), "AUDIT: ", log.Ldate|log.Ltime)
}

// --- XOR Stream Logic ---
type xorStream struct {
	inner io.ReadWriter
	key   []byte
	pos   int
}

func newXorStream(rw io.ReadWriter, key string) *xorStream {
	return &xorStream{inner: rw, key: []byte(key), pos: 0}
}

func (x *xorStream) Read(p []byte) (n int, err error) {
	n, err = x.inner.Read(p)
	for i := 0; i < n; i++ {
		p[i] ^= x.key[x.pos%len(x.key)]
		x.pos++
	}
	return
}

func (x *xorStream) Write(p []byte) (n int, err error) {
	buf := make([]byte, len(p))
	for i := 0; i < len(p); i++ {
		buf[i] = p[i] ^ x.key[x.pos%len(x.key)]
		x.pos++
	}
	return x.inner.Write(buf)
}

// --- Main Logic ---
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if Mode == "server" {
		runServer(ctx)
	} else {
		runClient(ctx)
	}

	// Wait for all active connections to finish or the timeout
	done := make(chan struct{})
	go func() {
		activeConns.Wait()
		close(done)
	}()

	select {
	case <-done:
		auditLog.Println("Shutdown Complete.")
	case <-time.After(5 * time.Second):
		auditLog.Println("Shutdown Timed-Out. Forcing Exit.")
	}
}

// --- Server Implementation ---
func runServer(ctx context.Context) {
	ln, err := net.Listen("tcp", ServerListenAddr)
	if err != nil {
		log.Fatal(err)
	}

	// Listener closer
	go func() {
		<-ctx.Done()
		auditLog.Println("Shutting Down Server Listener...")
		ln.Close()
	}()

	auditLog.Printf("Server Listening on %s", ServerListenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		activeConns.Add(1)
		go func() {
			defer activeConns.Done()
			handleServer(ctx, conn)
		}()
	}
}

func handleServer(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Ensure the connection closes
	// If the global context is canceled
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	conn.SetDeadline(time.Now().Add(DefaultTimeout))

	// 1. SMTP Handshake
	fmt.Fprintf(conn, "220 mail.google.com ESMTP\r\n")
	line := make([]byte, 1024)

	n, err := conn.Read(line)
	if err != nil || !strings.Contains(string(line[:n]), AuthSecret) {
		return
	}
	fmt.Fprintf(conn, "250-OK\r\n250 STARTTLS\r\n")

	n, err = conn.Read(line)
	if err != nil || !strings.Contains(string(line[:n]), "DATA") {
		return
	}
	fmt.Fprintf(conn, "354 Go ahead\r\n")

	conn.SetDeadline(time.Time{})
	stream := newXorStream(conn, AuthSecret)

	// 2. SOCKS5 Negotiation
	target, err := negotiateSocks5Server(stream)
	if err != nil {
		return
	}

	// 3. Connect to Target
	dest, err := net.DialTimeout("tcp", target, DefaultTimeout)
	if err != nil {
		auditLog.Printf("Failed to Reach %s", target)
		return
	}
	defer dest.Close()

	auditLog.Printf("Tunnel: %s -> %s", conn.RemoteAddr(), target)
	relay(stream, dest)
}

func negotiateSocks5Server(rw io.ReadWriter) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(rw, header); err != nil {
		return "", err
	}

	if header[0] != 0x05 {
		return "", fmt.Errorf("Wrong SOCKS Version")
	}

	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(rw, methods); err != nil {
		return "", err
	}

	rw.Write([]byte{0x05, 0x00})

	req := make([]byte, 4)
	if _, err := io.ReadFull(rw, req); err != nil {
		return "", err
	}

	var host string
	switch req[3] {
	case 0x01:
		ip := make([]byte, 4)
		io.ReadFull(rw, ip)

		host = net.IP(ip).String()

	case 0x03:
		lBuf := make([]byte, 1)
		io.ReadFull(rw, lBuf)

		domain := make([]byte, int(lBuf[0]))

		io.ReadFull(rw, domain)
		host = string(domain)

	default:
		return "", fmt.Errorf("Unsupported ATYP")
	}

	pBuf := make([]byte, 2)
	io.ReadFull(rw, pBuf)
	port := binary.BigEndian.Uint16(pBuf)

	rw.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return fmt.Sprintf("%s:%d", host, port), nil
}

// --- Client Implementation ---
func runClient(ctx context.Context) {
	ln, err := net.Listen("tcp", ClientListenAddr)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		<-ctx.Done()
		auditLog.Println("Shutting Down Client Listener...")
		ln.Close()
	}()

	log.Printf("Client Listening on %s -> Tunnel to %s", ClientListenAddr, ClientRemoteAddr)

	for {
		local, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		activeConns.Go(func() {
			handleClient(ctx, local)
		})
	}
}

func handleClient(ctx context.Context, local net.Conn) {
	defer local.Close()

	go func() {
		<-ctx.Done()
		local.Close()
	}()

	remote, err := net.DialTimeout("tcp", ClientRemoteAddr, DefaultTimeout)
	if err != nil {
		return
	}
	defer remote.Close()

	if !clientHandshake(remote) {
		return
	}

	stream := newXorStream(remote, AuthSecret)
	relay(local, stream)
}

func clientHandshake(conn net.Conn) bool {
	conn.SetDeadline(time.Now().Add(DefaultTimeout))
	defer conn.SetDeadline(time.Time{})

	buf := make([]byte, 1024)

	n, err := conn.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "220") {
		return false
	}
	fmt.Fprintf(conn, "EHLO %s\r\n", AuthSecret)

	n, err = conn.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "250") {
		return false
	}
	fmt.Fprintf(conn, "DATA\r\n")

	n, err = conn.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "354") {
		return false
	}

	return true
}

// --- Unified Relay ---
func relay(conn1, conn2 io.ReadWriter) {
	var wg sync.WaitGroup
	wg.Add(2)

	copyFunc := func(dst, src io.ReadWriter) {
		defer wg.Done()

		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		io.CopyBuffer(dst, src, buf)

		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		} else if c, ok := dst.(net.Conn); ok {
			c.Close()
		}
	}

	go copyFunc(conn1, conn2)
	go copyFunc(conn2, conn1)

	wg.Wait()
}
