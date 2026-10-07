package xorstream

import (
	"bytes"
	"testing"
)

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
