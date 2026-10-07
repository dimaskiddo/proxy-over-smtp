package tunnel

import (
	"fmt"
	"io"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/aesstream"
	"github.com/dimaskiddo/proxy-over-smtp/pkg/xorstream"
)

// wrapStream protects the post-handshake connection with the configured cipher. Both ends must
// use the same cipher and secret. The server swaps the derived keys, so each side seals with its
// own key and opens with the peer's.
func (t *Tunnel) wrapStream(rw io.ReadWriter, nonce []byte, server bool) (io.ReadWriteCloser, error) {
	if t.cfg.Cipher == config.CipherAES {
		k := deriveKeys(t.cfg.Secret, nonce)

		write, read := k.c2s, k.s2c
		if server {
			write, read = k.s2c, k.c2s
		}

		s, err := aesstream.New(rw, write[:], read[:])
		if err != nil {
			return nil, fmt.Errorf("aes stream: %w", err)
		}

		return s, nil
	}

	s, err := xorstream.New(rw, t.cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("xor stream: %w", err)
	}

	return s, nil
}
