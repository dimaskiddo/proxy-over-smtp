package relay

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestPipe(t *testing.T) {
	tests := []struct {
		name  string
		close func(a, b net.Conn)
	}{
		{"left closes", func(a, _ net.Conn) { a.Close() }},
		{"right closes", func(_, b net.Conn) { b.Close() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a1, a2 := net.Pipe()
			b1, b2 := net.Pipe()

			done := make(chan struct{})
			go func() {
				Pipe(a2, b1)
				close(done)
			}()

			msg := []byte("ping through the pipe")

			go a1.Write(msg)

			got := make([]byte, len(msg))
			if _, err := io.ReadFull(b2, got); err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, msg) {
				t.Fatalf("got %q want %q", got, msg)
			}

			go b2.Write(msg)

			if _, err := io.ReadFull(a1, got); err != nil {
				t.Fatal(err)
			}

			tt.close(a1, b2)

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Pipe did not return")
			}

			// Both ends must be closed by Pipe.
			if _, err := a2.Write([]byte{0}); err == nil {
				t.Fatal("a2 still open")
			}

			if _, err := b1.Write([]byte{0}); err == nil {
				t.Fatal("b1 still open")
			}
		})
	}
}
