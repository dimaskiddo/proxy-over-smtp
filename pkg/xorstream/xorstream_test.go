package xorstream

import (
	"bytes"
	"io"
	"testing"
)

// discardRW swallows writes and never yields a byte, so key cost can be measured without a
// network or a buffer.
type discardRW struct{}

// Write discards p.
func (discardRW) Write(p []byte) (int, error) { return len(p), nil }

// Read reports EOF.
func (discardRW) Read([]byte) (int, error) { return 0, io.EOF }

// BenchmarkWrite measures the rolling-XOR seal cost at the relay's chunk size. Informational,
// not a gate.
func BenchmarkWrite(b *testing.B) {
	payload := make([]byte, 128*1024)

	w, err := New(discardRW{}, "benchsecret")
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := w.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	w, err := New(&wire, "key")
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("hello world, uneven chunks across the key boundary")

	for i := 0; i < len(msg); i += 7 {
		end := min(i+7, len(msg))
		if _, err := w.Write(msg[i:end]); err != nil {
			t.Fatal(err)
		}
	}

	if bytes.Equal(wire.Bytes(), msg) {
		t.Fatal("wire data not obfuscated")
	}

	r, err := New(&wire, "key")
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
		t.Fatalf("got %q want %q", got, msg)
	}
}

func TestEmptyKey(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, ""); err != ErrEmptyKey {
		t.Fatalf("got %v want %v", err, ErrEmptyKey)
	}
}

func TestMatchesReference(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		chunks []int
	}{
		{"short key odd chunks", "k", []int{1, 2, 3, 100}},
		{"key 7 chunk 3", "abcdefg", []int{3, 3, 3, 3, 3}},
		{"chunk equals key", "abcd", []int{4, 4, 4}},
		{"chunk larger than key", "abcd", []int{1, 9, 50, 1}},
		{"long key", "THIS_IS_A_MUCH_LONGER_SHARED_SECRET", []int{5, 40, 7, 200}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := 0
			for _, c := range tt.chunks {
				total += c
			}

			plain := make([]byte, total)
			for i := range plain {
				plain[i] = byte(i * 31)
			}

			want := make([]byte, total)
			for i := range plain {
				want[i] = plain[i] ^ tt.key[i%len(tt.key)]
			}

			var wire bytes.Buffer
			w, err := New(&wire, tt.key)
			if err != nil {
				t.Fatal(err)
			}

			off := 0
			for _, c := range tt.chunks {
				if _, err := w.Write(plain[off : off+c]); err != nil {
					t.Fatal(err)
				}

				off += c
			}

			if !bytes.Equal(wire.Bytes(), want) {
				t.Fatalf("write differs from reference")
			}

			r, err := New(bytes.NewBuffer(want), tt.key)
			if err != nil {
				t.Fatal(err)
			}

			got := make([]byte, 0, total)
			buf := make([]byte, 13)
			for {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)
				if err != nil {
					break
				}
			}

			if !bytes.Equal(got, plain) {
				t.Fatalf("read differs from plaintext")
			}
		})
	}
}
