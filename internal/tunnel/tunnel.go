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

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

const (
	// defaultTimeout bounds every pre-relay step: handshakes, proxy request parsing, dials and
	// the TLS handshake. It never applies to an established relay.
	defaultTimeout = 30 * time.Second

	// forceWait bounds how long Shutdown waits for handlers after it closed their connections.
	forceWait = 5 * time.Second

	// minBackoff and maxBackoff bound the retry delay after a failed Accept, for example when
	// the process runs out of file descriptors.
	minBackoff = 5 * time.Millisecond
	maxBackoff = time.Second
)

// errShutdown is returned when a session is requested after Shutdown has closed the pool.
var errShutdown = errors.New("tunnel is shut down")

// Tunnel is one server or client instance. Run it with RunServer or RunClient, then end it
// with Shutdown. A Tunnel must not be reused after Shutdown.
type Tunnel struct {
	cfg    config.Config
	log    *slog.Logger
	tlsCfg *tls.Config

	// conns tracks every handler so Shutdown can wait for them. active mirrors it because a
	// WaitGroup cannot report its count. shutMu guards shutting against spawn, so an Add never
	// races the Wait that Shutdown starts.
	conns    sync.WaitGroup
	active   atomic.Int64
	shutMu   sync.Mutex
	shutting bool

	// hard is cancelled by Shutdown to close every connection that outlived the drain.
	hard       context.Context
	hardCancel context.CancelFunc

	// poolMu guards slots, the client's session pool. A server never sets it. Each slot is one
	// TCP connection carrying one smux session, and a local connection is pinned to one slot, so
	// the pool multiplies aggregate throughput rather than the speed of a single flow.
	poolMu sync.Mutex
	slots  []*slot
	// closed is set by closeSession so a pick or a dial running alongside it stops instead of
	// opening a session that nothing would ever close.
	closed bool
}

// New returns a Tunnel for cfg. When cfg sets TLSCert and TLSKey the key pair is loaded now
// so a bad file fails at startup.
func New(cfg config.Config, logger *slog.Logger) (*Tunnel, error) {
	// Rejected here too, not only in Config.Validate, because an empty secret would otherwise
	// derive session keys from an empty HMAC key for any caller that skips validation.
	if cfg.Secret == "" {
		return nil, errors.New("secret is required")
	}

	// A caller that skips Validate gets the cap anyway, so an unset value can never mean
	// unlimited streams.
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = config.DefaultMaxStreams
	}

	// Both zero means the pool was never configured, which is how a server or a bare client
	// arrives. The defaults are filled in here rather than in the pool, where an unset maximum
	// would silently disable growth.
	if cfg.PoolMin == 0 && cfg.PoolMax == 0 {
		cfg.PoolMin, cfg.PoolMax = config.DefaultPoolMin, config.DefaultPoolMax
	}

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

// Shutdown waits for active handlers to finish and marks the Tunnel closed. RunServer and
// RunClient must already have returned, which stops new connections. When ctx ends first, every
// remaining connection is closed and Shutdown returns ctx.Err(). It returns nil after a clean
// drain, and is safe to call more than once or before any Run.
func (t *Tunnel) Shutdown(ctx context.Context) error {
	t.shutMu.Lock()
	t.shutting = true
	t.shutMu.Unlock()

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

	// Also runs after a clean drain: it releases the AfterFunc registrations and closes
	// sessions that are only idle.
	t.hardCancel()
	t.closeSession()

	if err != nil {
		// Give handlers a moment to notice their closed connections and exit before the
		// caller ends the process.
		select {
		case <-done:
		case <-time.After(forceWait):
		}
	}

	return err
}

// spawn runs fn as a tracked handler so Shutdown waits for it and Active counts it. Once
// Shutdown has started, fn is dropped instead: adding to a WaitGroup after Wait began is a
// misuse panic, and a dropped handler never leaks because it owns the connection it closes.
func (t *Tunnel) spawn(fn func()) bool {
	t.shutMu.Lock()
	if t.shutting {
		t.shutMu.Unlock()
		return false
	}

	t.conns.Add(1)
	t.shutMu.Unlock()

	t.active.Add(1)

	go func() {
		defer t.active.Add(-1)
		defer t.conns.Done()

		fn()
	}()

	return true
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
// them. Accept errors are retried with capped backoff. It refuses to start, or stops, once
// Shutdown has run, so a late connection is closed instead of handled.
func (t *Tunnel) acceptLoop(ctx context.Context, ln net.Listener, handle func(net.Conn)) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()

	t.shutMu.Lock()
	shut := t.shutting
	t.shutMu.Unlock()

	if shut {
		return errShutdown
	}

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

		// A forced drain closes the connection to unblock the handler. The returned stop
		// func, called on return, unregisters it so finished handlers do not leak callbacks.
		// spawn reports false when Shutdown already ran, and the connection must not leak.
		if !t.spawn(func() {
			defer context.AfterFunc(t.hard, func() { conn.Close() })()
			defer conn.Close()
			handle(conn)
		}) {
			conn.Close()
			return errShutdown
		}
	}
}
