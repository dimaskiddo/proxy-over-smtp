package tunnel

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"
)

// sockOpt is one setsockopt call.
type sockOpt struct {
	name       string
	level, opt int
	val        int
}

// setInts applies each option to the socket behind c and wraps the first failure with its name.
// The per-OS setsockopt supplies the platform call.
func setInts(c syscall.RawConn, opts []sockOpt) error {
	var serr error

	err := c.Control(func(fd uintptr) {
		for _, o := range opts {
			if e := setsockopt(fd, o.level, o.opt, o.val); e != nil {
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

// keepAlive detects dead peers on idle sockets: after 15s idle, 9 probes 15s apart, so about 150s
// to drop a half-open connection. smux keepalive usually fires first for the tunnel itself; this
// covers target sockets, which have no smux.
var keepAlive = net.KeepAliveConfig{
	Enable:   true,
	Idle:     15 * time.Second,
	Interval: 15 * time.Second,
	Count:    9,
}

// listen binds the configured address with the tuned socket options. Options are applied before
// bind so that accepted connections inherit them.
func (t *Tunnel) listen(ctx context.Context) (net.Listener, error) {
	lc := net.ListenConfig{Control: listenControl, KeepAliveConfig: keepAlive}

	return lc.Listen(ctx, "tcp", t.cfg.Listen)
}

// dialer returns a Dialer with the tuned socket options. acl, when set, runs first so a refused
// target never reaches the option calls.
func dialer(acl func(network, address string, c syscall.RawConn) error) *net.Dialer {
	return &net.Dialer{
		Timeout:         defaultTimeout,
		KeepAliveConfig: keepAlive,
		Control: func(network, address string, c syscall.RawConn) error {
			if acl != nil {
				if err := acl(network, address, c); err != nil {
					return err
				}
			}

			return tuneDial(network, address, c)
		},
	}
}
