package relay

import (
	"io"
	"sync"
)

const bufferSize = 32 * 1024

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, bufferSize)
		return &b
	},
}

// Pipe copies both directions and returns when the first direction ends.
func Pipe(conn1, conn2 io.ReadWriter) {
	errCh := make(chan error, 2)

	cp := func(dst io.Writer, src io.Reader) {
		bp := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bp)

		_, err := io.CopyBuffer(dst, src, *bp)
		errCh <- err
	}

	go cp(conn1, conn2)
	go cp(conn2, conn1)

	<-errCh
}
