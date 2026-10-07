//go:build unix

package tunnel

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// setsockopt sets one integer socket option on fd.
func setsockopt(fd uintptr, level, opt, val int) error {
	return unix.SetsockoptInt(int(fd), level, opt, val)
}

// Send and receive buffers are left to kernel autotuning: a fixed small buffer caps throughput at
// roughly buffer/RTT.
var dialOpts = []sockOpt{
	{"TCP_NODELAY", unix.IPPROTO_TCP, unix.TCP_NODELAY, 1},
}

// The reuse options come first: they only matter before bind.
var listenOpts = append([]sockOpt{
	{"SO_REUSEADDR", unix.SOL_SOCKET, unix.SO_REUSEADDR, 1},
	{"SO_REUSEPORT", unix.SOL_SOCKET, unix.SO_REUSEPORT, 1},
}, dialOpts...)

// listenControl tunes a listening socket before bind. SO_REUSEPORT lets a new binary bind the
// port while the old one is still draining, at the cost that a second accidental server on the
// same port also succeeds.
func listenControl(_, _ string, c syscall.RawConn) error { return setInts(c, listenOpts) }

// tuneDial tunes an outgoing socket before connect.
func tuneDial(_, _ string, c syscall.RawConn) error { return setInts(c, dialOpts) }
