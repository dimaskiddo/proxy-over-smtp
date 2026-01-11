package main

import (
	"io"
	"sync"
)

// --- XOR Implementation ---
type xorStream struct {
	inner io.ReadWriter
	key   []byte
	rPos  int
	wPos  int
	muR   sync.Mutex
	muW   sync.Mutex
}

func newXorStream(rw io.ReadWriter, key string) *xorStream {
	return &xorStream{
		inner: rw,
		key:   []byte(key),
	}
}

func (x *xorStream) Read(p []byte) (n int, err error) {
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

func (x *xorStream) Write(p []byte) (n int, err error) {
	x.muW.Lock()
	defer x.muW.Unlock()

	keyLen := len(x.key)
	buf := make([]byte, len(p))
	for i := 0; i < len(p); i++ {
		buf[i] = p[i] ^ x.key[(x.wPos+i)%keyLen]
	}

	n, err = x.inner.Write(buf)
	if n > 0 {
		x.wPos = (x.wPos + n) % keyLen
	}

	return n, err
}

func (x *xorStream) Close() error {
	if closer, ok := x.inner.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
