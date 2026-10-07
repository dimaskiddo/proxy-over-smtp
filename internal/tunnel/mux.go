package tunnel

import (
	"time"

	"github.com/xtaci/smux"
)

// muxConfig returns the smux settings both sides must share. Changing any value except the
// timeouts is a wire break between client and server.
func muxConfig() *smux.Config {
	conf := smux.DefaultConfig()

	// Version 2 has per-stream flow control, so a stalled stream cannot block the whole session.
	conf.Version = 2
	conf.MaxReceiveBuffer = 16 * 1024 * 1024
	conf.MaxStreamBuffer = 512 * 1024

	conf.KeepAliveDisabled = false
	conf.KeepAliveInterval = 15 * time.Second
	conf.KeepAliveTimeout = 60 * time.Second

	return conf
}
