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
	atCap := strings.Repeat("a", maxLine) + "\r\n"
	if got, err := readLine(bufio.NewReader(strings.NewReader(atCap)), maxLine); err != nil || len(got) != maxLine {
		t.Fatalf("line at cap: len %d, err %v", len(got), err)
	}

	overCap := strings.Repeat("a", maxLine+1) + "\r\n"
	if _, err := readLine(bufio.NewReader(strings.NewReader(overCap)), maxLine); !errors.Is(err, errLineTooLong) {
		t.Fatalf("err = %v, want errLineTooLong", err)
	}

	if got, err := readLine(bufio.NewReader(strings.NewReader("EHLO x\r\n")), maxLine); err != nil || got != "EHLO x" {
		t.Fatalf("got %q, err %v, want %q", got, err, "EHLO x")
	}

	// A bare LF is refused rather than trimmed: RFC 5321 §4.1.1.4 tells servers not to accept that
	// form, even in the name of robustness.
	if _, err := readLine(bufio.NewReader(strings.NewReader("EHLO x\n")), maxLine); !errors.Is(err, errBareLF) {
		t.Fatalf("bare LF: err = %v, want errBareLF", err)
	}
}

// TestHandshakeLineCap checks that an over-long line is answered with 500, the reply RFC 5321
// §4.2.2 gives for a command line that is too long, and then dropped rather than buffered.
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

	r := bufio.NewReader(c)

	// The server greets before it reads, so the greeting must be consumed first.
	if _, err := readLine(r, maxLine); err != nil {
		t.Fatal(err)
	}

	go io.WriteString(c, strings.Repeat("A", maxLine+1)+"\r\n")

	// The 500 has to be drained before the handshake error is read: that write blocks until it is.
	if got, err := readLine(r, maxLine); err != nil || got != replySyntax {
		t.Fatalf("reply = %q, err %v, want %q", got, err, replySyntax)
	}

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
