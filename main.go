package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultTimeout = 30 * time.Second
	BufferSize     = 32 * 1024
)

var (
	auditLog    *log.Logger
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
		auditLog.Println("Shutdown Complete")
	case <-time.After(5 * time.Second):
		auditLog.Println("Shutdown Timed-Out. Forcing Exit")
	}
}
