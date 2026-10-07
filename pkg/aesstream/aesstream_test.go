package aesstream

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
)

// discardRW is an io.ReadWriter that swallows writes and never yields a byte, so seal cost can be
// measured without a network or a buffer.
type discardRW struct{}

// Write discards p.
func (discardRW) Write(p []byte) (int, error) { return len(p), nil }

// Read reports EOF.
func (discardRW) Read([]byte) (int, error) { return 0, io.EOF }

// BenchmarkWrite measures seal cost with one record per 128KB write, which is what the relay
// hands it. Informational, not a gate.
func BenchmarkWrite(b *testing.B) {
	c2s, s2c := keys(b)

	payload := make([]byte, 128*1024)
	if _, err := rand.Read(payload); err != nil {
		b.Fatal(err)
	}

	s, err := New(discardRW{}, c2s, s2c)
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := s.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// keys returns two distinct 32-byte key pairs for the two directions.
func keys(t testing.TB) (c2s, s2c []byte) {
	t.Helper()

	c2s = make([]byte, 32)
	s2c = make([]byte, 32)

	if _, err := rand.Read(c2s); err != nil {
		t.Fatal(err)
	}

	if _, err := rand.Read(s2c); err != nil {
		t.Fatal(err)
	}

	return c2s, s2c
}

func TestRoundTrip(t *testing.T) {
	c2s, s2c := keys(t)

	var wire bytes.Buffer
	w, err := New(&wire, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	msg := bytes.Repeat([]byte("hello world, uneven chunks across record boundaries "), 700)

	// Write in chunks that straddle the maxRecord boundary.
	for i := 0; i < len(msg); i += 4001 {
		end := min(i+4001, len(msg))
		if _, err := w.Write(msg[i:end]); err != nil {
			t.Fatal(err)
		}
	}

	if bytes.Contains(wire.Bytes(), msg[:64]) {
		t.Fatal("wire data contains plaintext")
	}

	r, err := New(&wire, s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 0, len(msg))
	buf := make([]byte, 5)
	for len(got) < len(msg) {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}

	if !bytes.Equal(got, msg) {
		t.Fatalf("got %d bytes want %d", len(got), len(msg))
	}
}

// TestRecordBoundaries writes each size in one call and reads it back one byte at a time, so a
// coalesced header and body must still split correctly across record boundaries.
func TestRecordBoundaries(t *testing.T) {
	sizes := []int{0, 1, maxRecord - 1, maxRecord, maxRecord + 1, 1 << 20}

	for _, size := range sizes {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c2s, s2c := keys(t)

			var wire bytes.Buffer
			w, err := New(&wire, c2s, s2c)
			if err != nil {
				t.Fatal(err)
			}

			msg := make([]byte, size)
			if _, err := rand.Read(msg); err != nil {
				t.Fatal(err)
			}

			if _, err := w.Write(msg); err != nil {
				t.Fatal(err)
			}

			// An empty write stays a no-op even after records are already on the wire.
			if n, err := w.Write(nil); n != 0 || err != nil {
				t.Fatalf("empty write = %d, %v", n, err)
			}

			r, err := New(&wire, s2c, c2s)
			if err != nil {
				t.Fatal(err)
			}

			got := make([]byte, 0, size)
			buf := make([]byte, 1)
			for len(got) < size {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)
				if err != nil {
					t.Fatalf("read %d of %d: %v", len(got), size, err)
				}
			}

			if !bytes.Equal(got, msg) {
				t.Fatalf("payload corrupted at size %d", size)
			}
		})
	}
}

func TestEmptyWriteThenRead(t *testing.T) {
	c2s, s2c := keys(t)

	var wire bytes.Buffer
	w, err := New(&wire, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write = %d, %v", n, err)
	}

	if wire.Len() != 0 {
		t.Fatalf("empty write produced %d bytes", wire.Len())
	}

	r, err := New(&wire, s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 8)); err != io.EOF {
		t.Fatalf("got %v want EOF on empty stream", err)
	}
}

func TestWrongKey(t *testing.T) {
	c2s, s2c := keys(t)

	var wire bytes.Buffer
	w, err := New(&wire, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("secret payload")); err != nil {
		t.Fatal(err)
	}

	other, _ := keys(t)

	r, err := New(&wire, other, other)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 32)); !errors.Is(err, errAuth) {
		t.Fatalf("got %v want errAuth", err)
	}
}

func TestTamperedTag(t *testing.T) {
	c2s, s2c := keys(t)

	var wire bytes.Buffer
	w, err := New(&wire, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("secret payload")); err != nil {
		t.Fatal(err)
	}

	bad := wire.Bytes()
	bad[len(bad)-1] ^= 0x01

	r, err := New(bytes.NewBuffer(bad), s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 32)); !errors.Is(err, errAuth) {
		t.Fatalf("got %v want errAuth", err)
	}
}

func TestRecordTooLarge(t *testing.T) {
	_, s2c := keys(t)

	// A prefix far over the limit must be refused before any body is read.
	frame := []byte{0xFF, 0xFF, 0xFF, 0xFF}

	r, err := New(bytes.NewBuffer(frame), s2c, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 16)); !errors.Is(err, errRecordTooLarge) {
		t.Fatalf("got %v want errRecordTooLarge", err)
	}
}

func TestTruncatedRecord(t *testing.T) {
	c2s, s2c := keys(t)

	var wire bytes.Buffer
	w, err := New(&wire, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("secret payload")); err != nil {
		t.Fatal(err)
	}

	bad := wire.Bytes()[:wire.Len()-1]

	r, err := New(bytes.NewBuffer(bad), s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 32)); !errors.Is(err, errShortRecord) {
		t.Fatalf("got %v want errShortRecord", err)
	}
}

// failWriter fails every write, so no record ever reaches the wire.
type failWriter struct{}

// Write always fails.
func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

// Read never returns data.
func (failWriter) Read([]byte) (int, error) { return 0, io.EOF }

// TestFailedWriteKeepsCounter checks that a record that never landed does not burn a sequence
// number, so the counter stays equal to the records the peer can have seen.
func TestFailedWriteKeepsCounter(t *testing.T) {
	c2s, s2c := keys(t)

	s, err := New(failWriter{}, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Write([]byte("payload")); err == nil {
		t.Fatal("want a write error")
	}

	if s.wseq != 0 {
		t.Fatalf("wseq = %d, want 0", s.wseq)
	}
}

// dripWriter accepts at most one byte per call, which is legal for an io.Writer.
type dripWriter struct {
	buf bytes.Buffer
}

// Write stores one byte and reports a short write.
func (d *dripWriter) Write(p []byte) (int, error) { return d.buf.Write(p[:min(1, len(p))]) }

// Read always reports EOF.
func (d *dripWriter) Read([]byte) (int, error) { return 0, io.EOF }

// TestShortWrites checks that a record is written in full even when the inner writer accepts one
// byte at a time.
func TestShortWrites(t *testing.T) {
	c2s, s2c := keys(t)

	inner := &dripWriter{}
	w, err := New(inner, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("short writes must still land whole")

	if _, err := w.Write(msg); err != nil {
		t.Fatal(err)
	}

	r, err := New(bytes.NewBuffer(inner.buf.Bytes()), s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len(msg))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, msg) {
		t.Fatalf("got %q want %q", got, msg)
	}
}

func TestConcurrentDuplex(t *testing.T) {
	c2s, s2c := keys(t)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	client, err := New(c1, c2s, s2c)
	if err != nil {
		t.Fatal(err)
	}

	server, err := New(c2, s2c, c2s)
	if err != nil {
		t.Fatal(err)
	}

	// The server echoes every decrypted byte back, so one connection exercises both directions
	// at once.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if n > 0 {
				if _, werr := server.Write(buf[:n]); werr != nil {
					return
				}
			}

			if err != nil {
				return
			}
		}
	}()

	msg := bytes.Repeat([]byte("duplex "), 5000)

	go func() {
		if _, err := client.Write(msg); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	got := make([]byte, 0, len(msg))
	buf := make([]byte, 1024)
	for len(got) < len(msg) {
		n, err := client.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Errorf("read: %v", err)
			break
		}
	}

	if !bytes.Equal(got, msg) {
		t.Fatalf("got %d bytes want %d", len(got), len(msg))
	}
}
