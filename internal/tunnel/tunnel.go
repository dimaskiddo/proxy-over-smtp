package tunnel

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

const (
	defaultTimeout = 30 * time.Second

	minBackoff = 5 * time.Millisecond
	maxBackoff = time.Second
)

type Tunnel struct {
	cfg   config.Config
	log   *slog.Logger
	conns sync.WaitGroup

	sessMu sync.Mutex
	sess   *smux.Session
}

func New(cfg config.Config, logger *slog.Logger) *Tunnel {
	return &Tunnel{cfg: cfg, log: logger}
}

// Wait blocks until all active connections finish.
func (t *Tunnel) Wait() {
	t.conns.Wait()
}

// bufConn keeps bytes already buffered by the handshake reader and still closes the socket.
type bufConn struct {
	r *bufio.Reader
	net.Conn
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// acceptLoop returns nil once ctx is done. Accept errors are retried with capped backoff.
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

		t.conns.Go(func() {
			defer context.AfterFunc(ctx, func() { conn.Close() })()
			defer conn.Close()
			handle(conn)
		})
	}
}
