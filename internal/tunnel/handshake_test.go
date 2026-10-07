package tunnel

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

func TestReadLine(t *testing.T) {
	atCap := strings.Repeat("a", maxLine) + "\n"
	if got, err := readLine(bufio.NewReader(strings.NewReader(atCap)), maxLine); err != nil || len(got) != maxLine {
		t.Fatalf("line at cap: len %d, err %v", len(got), err)
	}

	overCap := strings.Repeat("a", maxLine+1) + "\n"
	if _, err := readLine(bufio.NewReader(strings.NewReader(overCap)), maxLine); !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v, want errLineTooLong", err)
	}

	if got, err := readLine(bufio.NewReader(strings.NewReader("EHLO x\r\n")), maxLine); err != nil || got != "EHLO x" {
		t.Fatalf("got %q, err %v, want %q", got, err, "EHLO x")
	}
}

// TestHandshakeLineCap checks that an oversized EHLO is rejected instead of buffered.
func TestHandshakeLineCap(t *testing.T) {
	srv, err := New(config.Config{Secret: "s3cret", Cipher: config.CipherAES}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	c, s := net.Pipe()
	defer c.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := srv.serverHandshake(s, bufio.NewReader(s))
		s.Close()
		errCh <- err
	}()

	// The server greets before it reads, so the greeting must be consumed first.
	if _, err := readLine(bufio.NewReader(c), maxLine); err != nil {
		t.Fatal(err)
	}

	go io.WriteString(c, strings.Repeat("A", maxLine+1)+"\r\n")

	if err := <-errCh; !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v, want errLineTooLong", err)
	}
}

func TestNonceUnique(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 200; i++ {
		n, err := newNonce()
		if err != nil {
			t.Fatal(err)
		}

		if len(n) != nonceLen {
			t.Fatalf("nonce length = %d, want %d", len(n), nonceLen)
		}

		if seen[string(n)] {
			t.Fatal("duplicate nonce")
		}

		seen[string(n)] = true
	}
}

func TestDeriveKeys(t *testing.T) {
	nonce := bytes.Repeat([]byte{0xAB}, nonceLen)

	base := deriveKeys("secret", nonce)

	if deriveKeys("secret", nonce) != base {
		t.Fatal("deriveKeys is not deterministic")
	}

	if base.c2s == base.s2c {
		t.Fatal("c2s and s2c must differ")
	}

	if deriveKeys("other", nonce) == base {
		t.Fatal("a different secret must derive different keys")
	}

	if deriveKeys("secret", bytes.Repeat([]byte{0xCD}, nonceLen)) == base {
		t.Fatal("a different nonce must derive different keys")
	}
}

func TestProofValue(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x11}, nonceLen)

	base := proofValue("secret", nonce)

	if proofValue("secret", nonce) != base {
		t.Fatal("proofValue is not deterministic")
	}

	if proofValue("other", nonce) == base {
		t.Fatal("a different secret must produce a different proof")
	}

	if proofValue("secret", bytes.Repeat([]byte{0x22}, nonceLen)) == base {
		t.Fatal("a different nonce must produce a different proof")
	}
}
