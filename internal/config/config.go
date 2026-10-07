package config

import (
	"errors"
	"flag"
	"fmt"
)

// DefaultSecret is the flag default. Deployments must override it.
const DefaultSecret = "THIS_IS_YOUR_SECRET_WORD"

type Config struct {
	Mode             string
	ClientListenAddr string
	ClientRemoteAddr string
	ServerListenAddr string
	AuthSecret       string
	AuditLogFile     string
	AllowPrivate     bool
}

func Parse() Config {
	var c Config

	flag.StringVar(&c.Mode, "mode", "server", "Mode: 'client' or 'server'")
	flag.StringVar(&c.ClientListenAddr, "client", "0.0.0.0:1080", "Client listen address")
	flag.StringVar(&c.ClientRemoteAddr, "remote", "127.0.0.1:465", "Server remote address")
	flag.StringVar(&c.ServerListenAddr, "server", "0.0.0.0:465", "Server listen address")
	flag.StringVar(&c.AuthSecret, "secret", DefaultSecret, "Authentication secret")
	flag.StringVar(&c.AuditLogFile, "log-file", "./proxy-over-smtp.log", "Log file path")
	flag.BoolVar(&c.AllowPrivate, "allow-private", false, "Server: allow targets in loopback, private and link-local ranges")
	flag.Parse()

	return c
}

// Validate rejects settings that would crash or weaken the tunnel.
func (c Config) Validate() error {
	if c.Mode != "server" && c.Mode != "client" {
		return fmt.Errorf("invalid mode %q: want 'server' or 'client'", c.Mode)
	}

	if c.AuthSecret == "" {
		return errors.New("secret must not be empty")
	}

	return nil
}
