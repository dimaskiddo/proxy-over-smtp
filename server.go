package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

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
	reader := bufio.NewReader(conn)

	// 1. SMTP Handshake
	fmt.Fprintf(conn, "220 mail.google.com ESMTP\r\n")

	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, AuthSecret) {
		return
	}

	fmt.Fprintf(conn, "250-OK\r\n250 STARTTLS\r\n")

	line, err = reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "DATA") {
		return
	}

	fmt.Fprintf(conn, "354 Go ahead\r\n")
	conn.SetDeadline(time.Time{})

	// 2. XOR Strem with Multiplexer
	rw := &struct {
		io.Reader
		io.Writer
	}{reader, conn}

	stream := newXorStream(rw, AuthSecret)

	conf := yamux.DefaultConfig()
	conf.KeepAliveInterval = 15 * time.Second

	sess, err := yamux.Server(stream, conf)
	if err != nil {
		return
	}

	for {
		// 3. Virtual Stream Inside Multiplexer Connection
		vStream, err := sess.Accept()
		if err != nil {
			break
		}

		go func(vs net.Conn) {
			defer vs.Close()

			// 4. SOCKS5 Negotiation
			target, err := negotiateSocks5Server(vs)
			if err != nil {
				return
			}

			// 4. Connect to Target
			dest, err := net.DialTimeout("tcp", target, DefaultTimeout)
			if err != nil {
				auditLog.Printf("Failed to Reach %s", target)
				return
			}
			defer dest.Close()

			auditLog.Printf("Tunnel: %s -> %s", conn.RemoteAddr(), target)
			relay(vs, dest)
		}(vStream)
	}
}
