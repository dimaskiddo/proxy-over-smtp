package config

import "errors"

type Config struct {
	Listen       string
	Remote       string
	Secret       string
	AllowPrivate bool
}

// Validate rejects settings that would crash or weaken the tunnel.
func (c Config) Validate() error {
	if c.Secret == "" {
		return errors.New("secret is required: set --secret or PROXY_OVER_SMTP_SECRET")
	}

	if c.Listen == "" {
		return errors.New("listen address must not be empty")
	}

	return nil
}
