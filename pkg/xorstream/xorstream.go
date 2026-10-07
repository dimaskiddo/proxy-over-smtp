package xorstream

import (
	"errors"
	"io"
	"sync"
)

// ErrEmptyKey is returned by New when the key has no bytes.
var ErrEmptyKey = errors.New("xorstream: empty key")

// Stream XORs everything read and written with a rolling key. Obfuscation only.
type Stream struct {
	inner io.ReadWriter
	key   []byte
	rPos  int
	wPos  int
	wbuf  []byte
	muR   sync.Mutex
	muW   sync.Mutex
}

// New wraps rw. Both ends must use the same key.
func New(rw io.ReadWriter, key string) (*Stream, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}

	return &Stream{
		inner: rw,
		key:   []byte(key),
	}, nil
}

func (x *Stream) Read(p []byte) (n int, err error) {
	x.muR.Lock()
	defer x.muR.Unlock()

	n, err = x.inner.Read(p)
	if n > 0 {
		keyLen := len(x.key)
		for i := 0; i < n; i++ {
			p[i] ^= x.key[(x.rPos+i)%keyLen]
		}

		x.rPos = (x.rPos + n) % keyLen
	}

	return
}

func (x *Stream) Write(p []byte) (n int, err error) {
	x.muW.Lock()
	defer x.muW.Unlock()

	keyLen := len(x.key)
	if cap(x.wbuf) < len(p) {
		x.wbuf = make([]byte, len(p))
	}

	buf := x.wbuf[:len(p)]
	for i := range p {
		buf[i] = p[i] ^ x.key[(x.wPos+i)%keyLen]
	}

	n, err = x.inner.Write(buf)
	if n > 0 {
		x.wPos = (x.wPos + n) % keyLen
	}

	return n, err
}

// Close closes the inner stream when it is an io.Closer.
func (x *Stream) Close() error {
	if closer, ok := x.inner.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
