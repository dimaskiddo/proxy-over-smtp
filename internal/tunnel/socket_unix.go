//go:build unix

package tunnel

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// setInts applies each option to the socket behind c and wraps the first failure with its name.
func setInts(c syscall.RawConn, opts []sockOpt) error {
	var serr error

	err := c.Control(func(fd uintptr) {
		for _, o := range opts {
			if e := unix.SetsockoptInt(int(fd), o.level, o.opt, o.val); e != nil {
				serr = fmt.Errorf("set %s: %w", o.name, e)
				return
			}
		}
	})
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}

	return serr
}

// sockOpt is one setsockopt call.
type sockOpt struct {
	name       string
	level, opt int
	val        int
}

var dialOpts = []sockOpt{
	{"SO_RCVBUF", unix.SOL_SOCKET, unix.SO_RCVBUF, sockBuffer},
	{"SO_SNDBUF", unix.SOL_SOCKET, unix.SO_SNDBUF, sockBuffer},
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
