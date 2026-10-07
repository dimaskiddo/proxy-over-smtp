//go:build windows

package tunnel

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// setsockopt sets one integer socket option on fd.
func setsockopt(fd uintptr, level, opt, val int) error {
	return windows.SetsockoptInt(windows.Handle(fd), level, opt, val)
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
