package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
	"github.com/dimaskiddo/proxy-over-smtp/internal/tunnel"
)

func main() {
	cfg := config.Parse()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	f, err := os.OpenFile(cfg.AuditLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	auditLog := log.New(io.MultiWriter(os.Stdout, f), "AUDIT: ", log.Ldate|log.Ltime)

	if cfg.AuthSecret == config.DefaultSecret {
		auditLog.Println("Warning: Using Default Secret, Set -secret")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t := tunnel.New(cfg, auditLog)

	run := t.RunClient
	if cfg.Mode == "server" {
		run = t.RunServer
	}

	if err := run(ctx); err != nil {
		log.Fatal(err)
	}

	// Wait for all active connections to finish or the timeout
	done := make(chan struct{})
	go func() {
		t.Wait()
		close(done)
	}()

	select {
	case <-done:
		auditLog.Println("Shutdown Complete")
	case <-time.After(5 * time.Second):
		auditLog.Println("Shutdown Timed-Out. Forcing Exit")
	}
}
