// Package config holds the settings shared by the tunnel server and client.
package config

import "errors"

// Config is the tunnel configuration collected from flags and environment variables.
type Config struct {
	// Listen is the address the server or client accepts connections on.
	Listen string
	// Remote is the server address the client dials. Unused by the server.
	Remote string
	// Secret is the EHLO token and the XOR key. It must never be logged.
	Secret string
	// AllowPrivate lets the server dial loopback, private and link-local targets.
	AllowPrivate bool
	// TLSCert and TLSKey are PEM files that enable a TLS proxy listener on the client.
	// Both or neither must be set.
	TLSCert string
	TLSKey  string
}

// Validate rejects settings that would crash or weaken the tunnel.
func (c Config) Validate() error {
	if c.Secret == "" {
		return errors.New("secret is required: set --secret or PROXY_OVER_SMTP_SECRET")
	}

	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}

	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls-cert and tls-key must be set together")
	}

	return nil
}
