package tunnel

import (
	"time"

	"github.com/xtaci/smux"
)

func muxConfig() *smux.Config {
	conf := smux.DefaultConfig()

	conf.KeepAliveDisabled = false
	conf.KeepAliveInterval = 15 * time.Second
	conf.KeepAliveTimeout = 60 * time.Second

	return conf
}
