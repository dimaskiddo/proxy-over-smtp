// Package config holds the settings shared by the tunnel server and client.
package config

import (
	"errors"
	"fmt"
	"net"
)

// Tunnel ciphers. The client and server must be set to the same value.
const (
	// CipherXOR wraps the stream in rolling-key XOR: obfuscation only, no confidentiality.
	CipherXOR = "xor"
	// CipherAES wraps the stream in AES-256-GCM records: confidentiality and integrity.
	CipherAES = "aes"
)

// DefaultMaxStreams is how many streams one session may carry at once. It bounds the memory a
// single authenticated peer can hold in stream handlers.
const DefaultMaxStreams = 128

// Config is the tunnel configuration collected from flags and environment variables.
type Config struct {
	// Listen is the address the server or client accepts connections on.
	Listen string
	// Remote is the server address the client dials. Unused by the server.
	Remote string
	// Secret authenticates the handshake and seeds the stream keys. It is never transmitted
	// and must never be logged.
	Secret string
	// Cipher selects the stream protection. The client and server must match.
	Cipher string
	// AllowPrivate lets the server dial loopback, private and link-local targets.
	AllowPrivate bool
	// MaxStreams caps concurrent streams per session. The server enforces it; a client value
	// is accepted for symmetry but ignored.
	MaxStreams int
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

	if c.Cipher != CipherXOR && c.Cipher != CipherAES {
		return errors.New("cipher must be xor or aes")
	}

	if c.MaxStreams <= 0 {
		return errors.New("max-streams must be greater than zero")
	}

	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}

	// SplitHostPort only checks the shape: a bad address fails here instead of at listen time,
	// without turning a transient DNS failure into a startup failure.
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.Listen, err)
	}

	// Remote is client-only and may be empty on the server, but a set value must be usable.
	if c.Remote != "" {
		if _, _, err := net.SplitHostPort(c.Remote); err != nil {
			return fmt.Errorf("invalid remote address %q: %w", c.Remote, err)
		}
	}

	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls-cert and tls-key must be set together")
	}

	return nil
}
