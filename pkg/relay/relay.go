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

// Pipe copies both directions. When either direction ends it closes both ends and
// returns only after both copies have exited.
func Pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}

	var wg sync.WaitGroup

	cp := func(dst io.Writer, src io.Reader) {
		defer wg.Done()
		defer closeBoth()

		bp := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bp)

		// Wrapping hides WriterTo/ReaderFrom, which would bypass the pooled buffer.
		_, _ = io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, *bp)
	}

	wg.Add(2)
	go cp(a, b)
	go cp(b, a)

	wg.Wait()
}
