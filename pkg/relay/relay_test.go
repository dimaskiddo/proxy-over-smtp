package relay

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
)

// BenchmarkPipe measures the copy path alone: pooled 128KB buffers over a loopback pipe. It is
// informational, not a gate.
func BenchmarkPipe(b *testing.B) {
	payload := make([]byte, bufferSize)
	b.SetBytes(int64(len(payload)))

	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a1.Close()
	defer b2.Close()

	go Pipe(a2, b1)

	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, bufferSize)

		for i := 0; i < b.N; i++ {
			if _, err := b2.Read(buf); err != nil {
				return
			}
		}
	}()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := a1.Write(payload); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()
	<-done
}

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

// TestPipeBulk checks both directions carry a payload larger than the pooled copy buffer and
// that every byte survives.
func TestPipeBulk(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a1.Close()
	defer b2.Close()

	done := make(chan struct{})
	go func() {
		Pipe(a2, b1)
		close(done)
	}()

	// Over three times the 128KB copy buffer, so it cannot fit in one copy.
	msg := make([]byte, 3*bufferSize+777)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}

	go func() {
		if _, err := a1.Write(msg); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(b2, got); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, msg) {
		t.Fatal("forward payload corrupted")
	}

	go func() {
		if _, err := b2.Write(msg); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	if _, err := io.ReadFull(a1, got); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, msg) {
		t.Fatal("reverse payload corrupted")
	}

	a1.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Pipe did not return after close")
	}
}
