// Package tunnel carries proxy traffic between a local client and a remote server over one
// multiplexed connection that opens with a fake SMTP session.
package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

const (
	defaultTimeout = 30 * time.Second

	// forceWait bounds how long Shutdown waits for handlers after it closed their connections.
	forceWait = 5 * time.Second

	minBackoff = 5 * time.Millisecond
	maxBackoff = time.Second
)

// Tunnel is one server or client instance. Run it with RunServer or RunClient, then end it
// with Shutdown. A Tunnel must not be reused after Shutdown.
type Tunnel struct {
	cfg    config.Config
	log    *slog.Logger
	tlsCfg *tls.Config

	conns  sync.WaitGroup
	active atomic.Int64

	// hard is cancelled by Shutdown to close every connection that outlived the drain.
	hard       context.Context
	hardCancel context.CancelFunc

	sessMu sync.Mutex
	sess   *smux.Session
}

// New returns a Tunnel for cfg. When cfg sets TLSCert and TLSKey the key pair is loaded now
// so a bad file fails at startup.
func New(cfg config.Config, logger *slog.Logger) (*Tunnel, error) {
	t := &Tunnel{cfg: cfg, log: logger}
	t.hard, t.hardCancel = context.WithCancel(context.Background())

	if cfg.TLSCert != "" {
		pair, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			t.hardCancel()
			return nil, fmt.Errorf("load tls key pair: %w", err)
		}

		t.tlsCfg = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}

	return t, nil
}

// Active returns the number of connection and stream handlers currently running.
func (t *Tunnel) Active() int64 {
	return t.active.Load()
}

// Shutdown waits for active handlers to finish. RunServer and RunClient must already have
// returned, which stops new connections. When ctx ends first, every remaining connection is
// closed and Shutdown returns ctx.Err(). It returns nil after a clean drain.
func (t *Tunnel) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		t.conns.Wait()
		close(done)
	}()

	var err error

	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}

	t.hardCancel()
	t.closeSession()

	if err != nil {
		select {
		case <-done:
		case <-time.After(forceWait):
		}
	}

	return err
}

// spawn runs fn as a tracked handler so Shutdown waits for it and Active counts it.
func (t *Tunnel) spawn(fn func()) {
	t.active.Add(1)

	t.conns.Go(func() {
		defer t.active.Add(-1)
		fn()
	})
}

// bufConn keeps bytes already buffered by a peeking reader and still closes the socket.
type bufConn struct {
	r *bufio.Reader
	net.Conn
}

// Read reads through the buffered reader so peeked bytes are not lost.
func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// acceptLoop accepts connections and runs handle for each until ctx is done, then returns nil.
// It only stops accepting: open connections keep running until they finish or Shutdown closes
// them. Accept errors are retried with capped backoff.
func (t *Tunnel) acceptLoop(ctx context.Context, ln net.Listener, handle func(net.Conn)) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	backoff := minBackoff
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			t.log.Warn("accept failed", "err", err)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}

			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = minBackoff

		t.spawn(func() {
			defer context.AfterFunc(t.hard, func() { conn.Close() })()
			defer conn.Close()
			handle(conn)
		})
	}
}
