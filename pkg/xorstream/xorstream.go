// Package xorstream wraps an io.ReadWriter with a rolling-key XOR. It hides plaintext from
// naive inspection and provides no confidentiality or integrity.
package xorstream

import (
	"errors"
	"io"
	"sync"
)

// errEmptyKey is returned by New when the key has no bytes. It stays unexported because no
// caller distinguishes it: the tunnel rejects an empty secret long before this point.
var errEmptyKey = errors.New("xorstream: empty key")

// Stream XORs everything read and written with a rolling key. Obfuscation only.
type Stream struct {
	inner io.ReadWriter
	key   []byte

	// Reads and writes keep separate offsets and locks, so a full-duplex stream never makes
	// one direction wait for the other.
	rPos int
	wPos int
	muR  sync.Mutex
	muW  sync.Mutex

	// wbuf is reused across writes so Write does not allocate each call.
	wbuf []byte
}

// New wraps rw. Both ends must use the same key.
func New(rw io.ReadWriter, key string) (*Stream, error) {
	if key == "" {
		return nil, errEmptyKey
	}

	return &Stream{
		inner: rw,
		key:   []byte(key),
	}, nil
}

// Read reads from the inner stream and decodes the bytes in place.
func (x *Stream) Read(p []byte) (n int, err error) {
	x.muR.Lock()
	defer x.muR.Unlock()

	n, err = x.inner.Read(p)
	if n > 0 {
		x.rPos = xorKey(p[:n], p[:n], x.key, x.rPos)
	}

	return
}

// Write encodes p into a scratch buffer, so the caller's slice is never modified, and writes
// it to the inner stream.
func (x *Stream) Write(p []byte) (n int, err error) {
	x.muW.Lock()
	defer x.muW.Unlock()

	if cap(x.wbuf) < len(p) {
		x.wbuf = make([]byte, len(p))
	}

	buf := x.wbuf[:len(p)]
	xorKey(buf, p, x.key, x.wPos)

	n, err = x.inner.Write(buf)

	// Advance by what was actually written. After a short write the peer has decoded only n
	// bytes, so both offsets must stay in step.
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

// xorKey writes src XOR key, starting at key offset pos, into dst and returns the offset after
// len(src) bytes. dst may alias src.
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
