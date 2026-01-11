package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
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
