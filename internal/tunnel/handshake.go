package tunnel

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// nonceLen is the size of the per-connection server challenge. It is long enough that it never
// repeats in practice, which is what keeps every session's derived keys distinct.
const nonceLen = 32

// maxLine caps one handshake line. The real lines are under 128 bytes; the cap only exists so a
// peer cannot make the server buffer without bound, because the deadline bounds time, not bytes.
const maxLine = 4 << 10

// errLineTooLong is returned when a handshake line exceeds maxLine.
var errLineTooLong = errors.New("handshake line too long")

// maxReplyLines caps the 250- continuation lines the client accepts before the final 250.
const maxReplyLines = 16

// Labels separate the HMAC uses so one value can never be replayed as another.
const (
	labelProof = "ehlo"
	labelC2S   = "c2s"
	labelS2C   = "s2c"
)

// errAuth is the single failure returned for a bad or missing proof, so a probe cannot tell a
// wrong secret from a malformed line.
var errAuth = errors.New("invalid ehlo")

// sessionKeys holds the directional stream keys derived from the secret and the server nonce.
// Each direction has its own key, so the two never share an AES-GCM nonce.
type sessionKeys struct {
	c2s [32]byte
	s2c [32]byte
}

// readLine reads one line up to max bytes, returning it without its terminator. It fails rather
// than growing the buffer, so an oversized line costs a rejection and not memory.
func readLine(r *bufio.Reader, max int) (string, error) {
	var buf []byte

	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}

		if b == '\n' {
			return strings.TrimSuffix(string(buf), "\r"), nil
		}

		if len(buf) >= max {
			return "", errLineTooLong
		}

		buf = append(buf, b)
	}
}

// newNonce returns a fresh random challenge for one connection.
func newNonce() ([]byte, error) {
	n := make([]byte, nonceLen)

	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	return n, nil
}

// hmacSum returns HMAC-SHA256(secret, label || nonce). It is the only place the secret is used,
// so the secret itself never reaches the wire.
func hmacSum(secret, label string, nonce []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(label))
	mac.Write(nonce)

	return mac.Sum(nil)
}

// proofValue returns the base64 client response for a nonce. Both sides compute it, so it
// authenticates the client without sending the secret.
func proofValue(secret string, nonce []byte) string {
	return base64.StdEncoding.EncodeToString(hmacSum(secret, labelProof, nonce))
}

// deriveKeys returns the directional keys for a session.
func deriveKeys(secret string, nonce []byte) sessionKeys {
	var k sessionKeys

	copy(k.c2s[:], hmacSum(secret, labelC2S, nonce))
	copy(k.s2c[:], hmacSum(secret, labelS2C, nonce))

	return k
}
