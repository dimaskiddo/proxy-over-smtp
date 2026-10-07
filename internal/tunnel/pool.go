package tunnel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtaci/smux"
)

const (
	// shrinkAfter is how long a session must have carried nothing before the pool may release
	// it. A briefly quiet slot keeps its connection instead of being torn down and dialed again.
	shrinkAfter = 60 * time.Second
	// minGrowAt floors the per-slot stream count that makes the pool grow, so a small
	// --max-streams does not grow a session that is barely used.
	minGrowAt = 4
	// growDivisor scales the grow threshold with --max-streams: a session is "loaded" once it
	// carries an eighth of the streams the server would allow on it.
	growDivisor = 8
)

// slot is one smux session in the client pool, with the bookkeeping that decides when the pool
// grows and when it shrinks. A server never has a pool.
type slot struct {
	// sess is the session. It is nil only while a dial for this slot is in flight. It is written
	// under poolMu and read under it, except by the stream path, which holds the slot steady
	// because a reserved stream keeps the slot out of the shrink scan.
	sess *smux.Session

	// dialing is closed when the in-flight dial finishes, successfully or not. A caller that
	// finds a slot still dialing waits for it instead of opening a second TCP connection.
	dialing chan struct{}

	// open is the number of streams this slot carries, including one a caller has reserved but
	// not yet opened. Streams are pinned to their slot for life.
	open atomic.Int64

	// idleSince is when the slot last dropped to zero streams, and is the clock the shrink uses.
	// It is zero until the slot has carried and released a stream.
	idleSince time.Time
}

// pooledStream is a stream pinned to its pool slot. Closing it decrements that slot's load
// exactly once, which is what lets an idle slot become a shrink candidate later.
type pooledStream struct {
	*smux.Stream

	t    *Tunnel
	slot *slot
	once sync.Once
}

// Close closes the stream and releases its slot reservation. The reservation is released even
// when the session is already gone, so the slot can still be counted down.
func (s *pooledStream) Close() error {
	err := s.Stream.Close()

	s.once.Do(func() {
		if s.slot.open.Add(-1) == 0 {
			s.t.slotIdle(s.slot)
		}
	})

	return err
}

// pickSlot reserves one stream on a session: the least loaded healthy slot, or a freshly dialed
// one when every slot is loaded and the pool has room. The reservation is counted while the
// caller opens its stream, so a slot being handed out is never chosen for shutdown.
//
// Growth is single-flight per slot: while a dial runs, other callers wait for it and then take
// the least loaded slot, so a burst of local connections opens one extra TCP connection rather
// than one each.
func (t *Tunnel) pickSlot(ctx context.Context) (*slot, error) {
	for {
		t.poolMu.Lock()

		if t.closed {
			t.poolMu.Unlock()
			return nil, errShutdown
		}

		t.dropLocked(func(s *slot) bool { return s.sess != nil && s.sess.IsClosed() })

		var waiting chan struct{}

		for _, s := range t.slots {
			if s.sess == nil {
				waiting = s.dialing
				break
			}
		}

		if waiting != nil {
			t.poolMu.Unlock()

			select {
			case <-waiting:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		pick := t.leastLoadedLocked()

		if pick != nil && !t.loadedLocked() {
			pick.open.Add(1)
			t.poolMu.Unlock()

			return pick, nil
		}

		if len(t.slots) >= t.cfg.PoolMax {
			if pick == nil {
				t.poolMu.Unlock()
				return nil, errors.New("no live tunnel session")
			}

			pick.open.Add(1)
			t.poolMu.Unlock()

			return pick, nil
		}

		s := &slot{dialing: make(chan struct{})}
		t.slots = append(t.slots, s)
		t.poolMu.Unlock()

		sess, err := t.dialSession(ctx)

		t.poolMu.Lock()
		close(s.dialing)

		if t.closed {
			t.poolMu.Unlock()

			// A dial that lands after closeSession has a session nothing would ever close. A
			// failed dial leaves it nil.
			if sess != nil {
				sess.Close()
			}

			return nil, errShutdown
		}

		if err != nil {
			t.dropLocked(func(x *slot) bool { return x == s })
		} else {
			s.sess = sess
		}
		t.poolMu.Unlock()

		if err != nil {
			// The growth was opportunistic, so an existing slot can still take the stream.
			if pick != nil {
				pick.open.Add(1)
				return pick, nil
			}

			return nil, err
		}

		s.open.Add(1)

		return s, nil
	}
}

// openStream opens one stream on a pooled session. The stream is pinned to its slot for life. A
// failed open evicts that slot and retries on another, because a restarted peer leaves a session
// that IsClosed still reports as open until keepalive times out.
func (t *Tunnel) openStream(ctx context.Context) (*pooledStream, error) {
	// One attempt per possible slot, plus one: each failure removes a slot, so the pool can only
	// shrink, and the extra attempt covers a slot that was replaced mid-loop.
	var err error

	for range t.cfg.PoolMax + 1 {
		var s *slot
		if s, err = t.pickSlot(ctx); err != nil {
			return nil, err
		}

		stream, oerr := s.sess.OpenStream()
		if oerr == nil {
			return &pooledStream{Stream: stream, t: t, slot: s}, nil
		}

		s.open.Add(-1)
		t.evict(s)

		err = oerr
	}

	return nil, err
}

// evict closes one slot and removes it from the pool. It is called when a stream cannot be
// opened on it.
func (t *Tunnel) evict(s *slot) {
	t.poolMu.Lock()
	t.dropLocked(func(x *slot) bool { return x == s })
	t.poolMu.Unlock()

	s.sess.Close()
}

// slotIdle marks a slot that just released its last stream, then tries to shrink the pool. Only
// a stream close calls this, so the pool releases a session because real traffic ended and never
// because a timer fired.
func (t *Tunnel) slotIdle(s *slot) {
	t.poolMu.Lock()

	found := false
	for _, x := range t.slots {
		if x == s {
			found = true
			break
		}
	}

	if !found {
		t.poolMu.Unlock()
		return
	}

	now := time.Now()
	s.idleSince = now

	// A slot that just became idle is at the front of the queue to be released, but only if it
	// stays idle for shrinkAfter, which the next stream close is what notices.
	live := t.slots[:0]
	var drop []*slot

	for _, x := range t.slots {
		idle := x.open.Load() == 0 && !x.idleSince.IsZero() && now.Sub(x.idleSince) >= shrinkAfter
		if idle && len(t.slots)-len(drop) > t.cfg.PoolMin {
			drop = append(drop, x)
			continue
		}

		live = append(live, x)
	}

	t.slots = live
	remaining := len(t.slots)
	t.poolMu.Unlock()

	for _, x := range drop {
		x.sess.Close()
	}

	if len(drop) > 0 {
		t.log.Debug("session pool shrunk", "closed", len(drop), "slots", remaining)
	}
}

// dropLocked removes every slot matching drop from the pool. Callers hold poolMu.
func (t *Tunnel) dropLocked(drop func(*slot) bool) {
	live := t.slots[:0]

	for _, s := range t.slots {
		if !drop(s) {
			live = append(live, s)
		}
	}

	t.slots = live
}

// leastLoadedLocked returns the healthy slot carrying the fewest streams, or nil when there is
// none. Callers hold poolMu.
func (t *Tunnel) leastLoadedLocked() *slot {
	var best *slot

	for _, s := range t.slots {
		if s.sess == nil || s.sess.IsClosed() {
			continue
		}

		if best == nil || s.open.Load() < best.open.Load() {
			best = s
		}
	}

	return best
}

// loadedLocked reports whether every slot carries enough streams that another session would help.
// Callers hold poolMu.
func (t *Tunnel) loadedLocked() bool {
	growAt := t.growAt()

	for _, s := range t.slots {
		if s.open.Load() < growAt {
			return false
		}
	}

	return true
}

// growAt is the per-slot stream count at which the pool considers every slot loaded.
func (t *Tunnel) growAt() int64 {
	return max(minGrowAt, int64(t.cfg.MaxStreams/growDivisor))
}
