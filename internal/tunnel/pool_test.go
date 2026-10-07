package tunnel

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtaci/smux"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

// poolSize reports how many sessions the client pool currently holds.
func poolSize(t *Tunnel) int {
	t.poolMu.Lock()
	defer t.poolMu.Unlock()

	return len(t.slots)
}

// poolSessions returns a copy of the live sessions in the client pool.
func poolSessions(t *Tunnel) []*smux.Session {
	t.poolMu.Lock()
	defer t.poolMu.Unlock()

	var out []*smux.Session

	for _, s := range t.slots {
		if s.sess != nil {
			out = append(out, s.sess)
		}
	}

	return out
}

// loadedPool returns a pair whose pool spans PoolMax sessions by opening enough streams to load
// every slot, plus the streams it opened. Callers close the returned streams.
func loadedPool(t *testing.T) (*pair, []io.ReadWriteCloser, net.Addr) {
	t.Helper()

	p := newPair(t, config.Config{MaxStreams: 8, PoolMin: 1, PoolMax: 3})
	echo := echoListener(t)
	target := echo.Addr().(net.Addr)

	// growAt is MaxStreams/growDivisor with a floor of 4, so four streams per slot make a slot
	// loaded and force the next stream onto a new session. Twelve streams span three slots.
	held := make([]io.ReadWriteCloser, 0, 12)
	for range 12 {
		held = append(held, socksConnect(t, p.cli, target))
	}

	if got := poolSize(p.cli); got != 3 {
		t.Fatalf("pool has %d sessions, want 3", got)
	}

	t.Cleanup(func() {
		for _, s := range held {
			s.Close()
		}
	})

	return p, held, target
}

// TestPoolDefaults checks a client that sets neither flag gets the documented bounds.
func TestPoolDefaults(t *testing.T) {
	cli := startPair(t)

	if cli.cfg.PoolMin != config.DefaultPoolMin || cli.cfg.PoolMax != config.DefaultPoolMax {
		t.Fatalf("pool = %d..%d, want %d..%d",
			cli.cfg.PoolMin, cli.cfg.PoolMax, config.DefaultPoolMin, config.DefaultPoolMax)
	}
}

// TestPoolGrowsUnderLoad checks the pool opens another session once every slot is loaded, and
// stops at the maximum.
func TestPoolGrowsUnderLoad(t *testing.T) {
	p := newPair(t, config.Config{MaxStreams: 8, PoolMin: 1, PoolMax: 3})
	echo := echoListener(t)

	if got := poolSize(p.cli); got != 0 {
		t.Fatalf("pool started with %d sessions, want 0: nothing may dial before a connection needs it", got)
	}

	held := make([]io.ReadWriteCloser, 0, 12)
	defer func() {
		for _, s := range held {
			s.Close()
		}
	}()

	// Four streams stay on one session: the pool grows on load, not on connection count.
	for range 4 {
		held = append(held, socksConnect(t, p.cli, echo.Addr()))
	}

	if got := poolSize(p.cli); got != 1 {
		t.Fatalf("pool has %d sessions after four streams, want 1", got)
	}

	for range 8 {
		held = append(held, socksConnect(t, p.cli, echo.Addr()))
	}

	if got := poolSize(p.cli); got != 3 {
		t.Fatalf("pool has %d sessions after twelve streams, want the maximum of 3", got)
	}
}

// TestPoolShrinksWhenIdle checks the pool releases sessions that have carried nothing for a
// while, down to PoolMin and no further.
func TestPoolShrinksWhenIdle(t *testing.T) {
	p, held, target := loadedPool(t)

	for _, s := range held {
		s.Close()
	}

	// Backdate the idle clock: shrinkAfter is a wall-clock threshold no test can wait out.
	p.cli.poolMu.Lock()
	for _, s := range p.cli.slots {
		s.idleSince = time.Now().Add(-shrinkAfter)
	}
	p.cli.poolMu.Unlock()

	// There is no shrink timer, so a stream close is what notices the idle slots.
	s := socksConnect(t, p.cli, target)
	s.Close()

	if got := poolSize(p.cli); got != 1 {
		t.Fatalf("pool has %d sessions after shrinking, want the minimum of 1", got)
	}
}

// TestPoolKeepsRecentlyUsedWhenShrinking checks the shrink stops at PoolMin even when every
// session is idle, so a quiet client keeps its pool instead of dropping to one connection.
func TestPoolKeepsRecentlyUsedWhenShrinking(t *testing.T) {
	p := newPair(t, config.Config{MaxStreams: 8, PoolMin: 2, PoolMax: 3})
	echo := echoListener(t)
	target := echo.Addr().(net.Addr)

	held := make([]io.ReadWriteCloser, 0, 12)
	for range 12 {
		held = append(held, socksConnect(t, p.cli, target))
	}

	for _, s := range held {
		s.Close()
	}

	p.cli.poolMu.Lock()
	for _, s := range p.cli.slots {
		s.idleSince = time.Now().Add(-shrinkAfter)
	}
	p.cli.poolMu.Unlock()

	s := socksConnect(t, p.cli, target)
	s.Close()

	if got := poolSize(p.cli); got != 2 {
		t.Fatalf("pool has %d sessions after shrinking, want the minimum of 2", got)
	}
}

// TestPoolRecoversFromDeadSessions checks a session that dies without the pool noticing is
// replaced, repeatedly, without the pool growing past its maximum.
func TestPoolRecoversFromDeadSessions(t *testing.T) {
	p := newPair(t, config.Config{PoolMin: 1, PoolMax: 2})
	echo := echoListener(t)

	s := socksConnect(t, p.cli, echo.Addr())
	s.Close()

	for i := range 5 {
		sessions := poolSessions(p.cli)
		if len(sessions) == 0 {
			t.Fatalf("round %d: pool is empty", i)
		}

		// The session dies under the pool, the way a restarted server leaves it: IsClosed only
		// turns true once the keepalive notices.
		sessions[0].Close()

		s := socksConnect(t, p.cli, echo.Addr())
		s.Close()
	}

	if got := poolSize(p.cli); got > 2 {
		t.Fatalf("pool has %d sessions, want at most the maximum of 2", got)
	}
}

// TestPoolConcurrentStreams runs many opens and closes at once. It is the shape the race
// detector needs to see: picks, growth, reservations and the shrink scan all racing.
func TestPoolConcurrentStreams(t *testing.T) {
	p := newPair(t, config.Config{MaxStreams: 8, PoolMin: 1, PoolMax: 4})

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() {
			s, err := p.cli.openStream(context.Background())
			if err != nil {
				t.Error(err)
				return
			}

			s.Close()
		})
	}

	wg.Wait()

	if got := poolSize(p.cli); got > 4 {
		t.Fatalf("pool has %d sessions, want at most the maximum of 4", got)
	}
}

// TestCloseSessionClosesPool checks shutdown closes every session and stops the pool from
// dialing a replacement.
func TestCloseSessionClosesPool(t *testing.T) {
	p, _, _ := loadedPool(t)

	p.cli.closeSession()

	if got := poolSize(p.cli); got != 0 {
		t.Fatalf("pool has %d sessions after closeSession, want 0", got)
	}

	if _, err := p.cli.openStream(context.Background()); err == nil {
		t.Fatal("openStream succeeded after closeSession, want a shutdown error")
	}
}
