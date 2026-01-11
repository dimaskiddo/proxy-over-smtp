package main

import (
	"io"
	"sync"
)

var (
	bufferPool = sync.Pool{
		New: func() interface{} { return make([]byte, BufferSize) },
	}
)

// --- Unified Relay Implementation ---
func relay(conn1, conn2 io.ReadWriter) {
	errCh := make(chan error, 2)

	go func() {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		_, err := io.CopyBuffer(conn1, conn2, buf)
		errCh <- err
	}()

	go func() {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)

		_, err := io.CopyBuffer(conn2, conn1, buf)
		errCh <- err
	}()

	<-errCh
}
