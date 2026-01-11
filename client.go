package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

var (
	session     *yamux.Session
	sessionLock sync.Mutex
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

	sess, err := getSession(ctx)
	if err != nil {
		return
	}

	remoteStream, err := sess.Open()
	if err != nil {
		return
	}
	defer remoteStream.Close()

	relay(local, remoteStream)
}

func getSession(ctx context.Context) (*yamux.Session, error) {
	sessionLock.Lock()
	defer sessionLock.Unlock()

	if session != nil && !session.IsClosed() {
		return session, nil
	}

	// 1. Connect to Server
	remote, err := net.DialTimeout("tcp", ClientRemoteAddr, DefaultTimeout)
	if err != nil {
		return nil, err
	}

	// 2. SMTP Handshake
	reader := bufio.NewReader(remote)
	if !clientHandshake(remote, reader) {
		remote.Close()
		return nil, fmt.Errorf("SMTP Handshake Failed")
	}

	// 3. XOR Strem with Multiplexer
	rw := &struct {
		io.Reader
		io.Writer
	}{reader, remote}

	stream := newXorStream(rw, AuthSecret)

	conf := yamux.DefaultConfig()
	conf.KeepAliveInterval = 15 * time.Second

	sess, err := yamux.Client(stream, conf)
	if err != nil {
		return nil, err
	}

	session = sess
	return session, nil
}

func clientHandshake(conn net.Conn, r *bufio.Reader) bool {
	conn.SetDeadline(time.Now().Add(DefaultTimeout))
	defer conn.SetDeadline(time.Time{})

	// SMTP Read 220
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "220") {
		return false
	}

	// SMTP Send EHLO
	fmt.Fprintf(conn, "EHLO %s\r\n", AuthSecret)

	// SMTP Read 250
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			return false
		}

		if strings.HasPrefix(line, "250 ") {
			break
		}
	}

	// SMTP Send Data
	fmt.Fprintf(conn, "DATA\r\n")

	// SMTP Read 354
	line, err = r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "354") {
		return false
	}

	return true
}
