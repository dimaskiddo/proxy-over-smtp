//go:build windows

package tunnel

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

// setInts applies each option to the socket behind c and wraps the first failure with its name.
func setInts(c syscall.RawConn, opts []sockOpt) error {
	var serr error

	err := c.Control(func(fd uintptr) {
		for _, o := range opts {
			if e := windows.SetsockoptInt(windows.Handle(fd), o.level, o.opt, o.val); e != nil {
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

// Windows has no SO_REUSEPORT, and its SO_REUSEADDR lets another process steal a bound port, so
// neither is set. Go keeps its own safe listener default there.
var opts = []sockOpt{
	{"SO_RCVBUF", windows.SOL_SOCKET, windows.SO_RCVBUF, sockBuffer},
	{"SO_SNDBUF", windows.SOL_SOCKET, windows.SO_SNDBUF, sockBuffer},
	{"TCP_NODELAY", windows.IPPROTO_TCP, windows.TCP_NODELAY, 1},
}

// listenControl tunes a listening socket before bind.
func listenControl(_, _ string, c syscall.RawConn) error { return setInts(c, opts) }

// tuneDial tunes an outgoing socket before connect.
func tuneDial(_, _ string, c syscall.RawConn) error { return setInts(c, opts) }
