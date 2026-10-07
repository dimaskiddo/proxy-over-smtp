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
		x.rPos = xorKey(p[:n], p[:n], x.key, x.rPos)
	}

	return
}

func (x *Stream) Write(p []byte) (n int, err error) {
	x.muW.Lock()
	defer x.muW.Unlock()

	if cap(x.wbuf) < len(p) {
		x.wbuf = make([]byte, len(p))
	}

	buf := x.wbuf[:len(p)]
	xorKey(buf, p, x.key, x.wPos)

	n, err = x.inner.Write(buf)
	if n > 0 {
		x.wPos = (x.wPos + n) % len(x.key)
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

// xorKey writes src XOR key (starting at offset pos) into dst and returns the offset after len(src) bytes.
func xorKey(dst, src, key []byte, pos int) int {
	for len(src) > 0 {
		k := key[pos:]
		n := min(len(k), len(src))

		for i := 0; i < n; i++ {
			dst[i] = src[i] ^ k[i]
		}

		dst, src = dst[n:], src[n:]
		pos = (pos + n) % len(key)
	}

	return pos
}
