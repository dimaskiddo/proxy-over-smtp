// Package relay copies bytes between two connections with pooled buffers.
package relay

import (
	"io"
	"sync"
)

// bufferSize is the size of each pooled copy buffer. Large enough that a bulk transfer spends its
// time in the kernel rather than in per-copy syscalls.
const bufferSize = 128 * 1024

// bufferPool recycles copy buffers so busy proxies do not allocate per connection.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, bufferSize)
		return &b
	},
}

// Pipe copies both directions. When either direction ends it closes both ends and
// returns only after both copies have exited.
func Pipe(a, b io.ReadWriteCloser) {
	PipeCount(a, b)
}

// PipeCount is Pipe that also reports the bytes copied in each direction: up is what b read
// from a, down is what a read from b. A caller that logs transfer sizes uses this instead of
// counting around Pipe.
func PipeCount(a, b io.ReadWriteCloser) (up, down int64) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}

	var wg sync.WaitGroup
	var n [2]int64

	cp := func(i int, dst io.Writer, src io.Reader) {
		defer wg.Done()
		defer closeBoth()

		bp := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bp)

		// Wrapping hides WriterTo/ReaderFrom, which would bypass the pooled buffer.
		n[i], _ = io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bp)
	}

	wg.Add(2)
	go cp(0, a, b)
	go cp(1, b, a)

	wg.Wait()

	// n[0] is b to a, so it is the down direction; n[1] is a to b, the up direction.
	return n[1], n[0]
}
