package main

import "io"

// --- XOR Implementation ---
type xorStream struct {
	inner io.ReadWriter
	key   []byte
	pos   int
}

func newXorStream(rw io.ReadWriter, key string) *xorStream {
	return &xorStream{inner: rw, key: []byte(key), pos: 0}
}

func (x *xorStream) Read(p []byte) (n int, err error) {
	n, err = x.inner.Read(p)
	if n > 0 {
		keyLen := len(x.key)
		for i := 0; i < n; i++ {
			p[i] ^= x.key[(x.pos+i)%keyLen]
		}

		x.pos = (x.pos + n) % keyLen
	}

	return
}

func (x *xorStream) Write(p []byte) (n int, err error) {
	keyLen := len(x.key)

	for i := 0; i < len(p); i++ {
		p[i] ^= x.key[(x.pos+i)%keyLen]
	}

	n, err = x.inner.Write(p)
	x.pos = (x.pos + n) % keyLen

	return
}

func (x *xorStream) Close() error {
	if closer, ok := x.inner.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
