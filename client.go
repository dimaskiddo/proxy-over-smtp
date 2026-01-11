package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

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
