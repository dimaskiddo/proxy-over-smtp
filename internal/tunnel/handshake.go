package tunnel

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// nonceLen is the size of the per-connection server challenge. It is long enough that it never
// repeats in practice, which is what keeps every session's derived keys distinct.
const nonceLen = 32

// maxLine caps one handshake line, counted in content bytes: the CR of the terminator is not one.
// The real lines are under 128 bytes, and the cap only exists so a peer cannot make the server
// buffer without bound, because the deadline bounds time, not bytes. It is well above the RFC 5321
// §4.5.3.1.4 512-octet command line limit, so no conforming client can reach it.
const maxLine = 4 << 10

// errLineTooLong is returned when a handshake line exceeds maxLine.
var errLineTooLong = errors.New("handshake line too long")

// errBareLF is returned for a line ended by a bare LF. RFC 5321 §4.1.1.4 tells servers not to accept
// that form, so a line which uses it is malformed rather than merely unusual.
var errBareLF = errors.New("line not terminated by CRLF")

// maxReplyLines caps the 250- continuation lines the client accepts before the final 250.
const maxReplyLines = 16

// Labels separate the HMAC uses so one value can never be replayed as another.
const (
	labelProof = "ehlo"
	labelC2S   = "c2s"
	labelS2C   = "s2c"
)

// The fake envelope both sides play out. Every client line has to parse as RFC 5321 syntax, so the
// EHLO argument is a plain host and the proof travels as an ESMTP parameter on MAIL FROM, where
// any visible character is legal. These are constants rather than derived values: a rotating
// envelope would read as a stranger's mail instead of a routine notification.
const (
	// ehloHost is the client's EHLO argument, an address literal in a documentation range, which
	// is the ordinary shape for a relay that has no reverse DNS name.
	ehloHost = "[192.0.2.10]"

	// mailHost is the server's domain, in the greeting and the EHLO response.
	mailHost = "smtp.gmail.com"

	// mailFrom and rcptTo give the transaction an envelope: a notification addressed to itself.
	mailFrom = "no-reply@gmail.com"
	rcptTo   = "no-reply@gmail.com"

	// extKeyword advertises the extension that carries the proof, so the X-PROOF parameter is a
	// known option instead of an unrecognized one.
	extKeyword = "X-PROOF"

	// ehloLine is the session-opening command the client sends, and rcptLine the recipient line.
	// The server only checks the verb: HELO is accepted alongside EHLO because RFC 5321 §4.1.1.1
	// keeps both, and a relay that answered only EHLO would look newer than the mail server it
	// imitates.
	ehloLine = "EHLO " + ehloHost
	rcptLine = "RCPT TO:<" + rcptTo + ">"

	// mailPrefix is the fixed part of the MAIL FROM line; proofValue is appended to it. Only the
	// appended value is secret, so only it is compared in constant time.
	mailPrefix = "MAIL FROM:<" + mailFrom + "> " + extKeyword + "="

	// replySyntax answers a malformed line and a bad proof with the same bytes, so no reply tells a
	// probe which of the two it sent. replyParam is the syntax error in the argument, which a real
	// relay names separately. replyUnimplemented refuses a command a real server recognizes but does
	// not offer, and replyBye ends the session.
	replyOK            = "250 OK"
	replySyntax        = "500 Syntax error, command unrecognized"
	replyParam         = "501 Syntax error in parameters or arguments"
	replyUnimplemented = "502 Command not implemented"

	// replySequence answers a command that arrived out of order.
	replySequence = "503 Bad sequence of commands"
	replyBye      = "221 Bye"
)

// errAuth is the single failure returned for a malformed line or a bad proof, each already answered
// with its own reply. A bad proof and a garbage line get identical 500 bytes, so neither reveals
// which case fired or whether the secret was close.
var errAuth = errors.New("invalid ehlo")

// errSequence reports a command that arrived out of order, already answered with 503. It and
// errUnsupported both end the session the same way; they differ only in what the log reports.
var errSequence = errors.New("bad sequence")

// errUnsupported reports a command the session answers but does not continue past, already replied
// to. A real server keeps the session open after NOOP and RSET; this one closes, because the tunnel
// client never sends them and an open session is one more thing to hold.
var errUnsupported = errors.New("unsupported command")

// errQuit reports that the peer sent QUIT and has already been answered with 221, so the caller
// closes without a second reply.
var errQuit = errors.New("quit")

// sessionKeys holds the directional stream keys derived from the secret and the server nonce.
// Each direction has its own key, so the two never share an AES-GCM nonce.
type sessionKeys struct {
	c2s [32]byte
	s2c [32]byte
}

// readLine reads one line of up to max content bytes, returning it without its CRLF terminator. A
// line ended by a bare LF is refused rather than trimmed, and an over-long line fails rather than
// growing the buffer, so neither costs memory.
func readLine(r *bufio.Reader, max int) (string, error) {
	var buf []byte

	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}

		if b == '\n' {
			if len(buf) == 0 || buf[len(buf)-1] != '\r' {
				return "", errBareLF
			}

			return string(buf[:len(buf)-1]), nil
		}

		// The buffer may hold one byte more than max: the CR of the terminator, which is stripped
		// rather than counted as content.
		if len(buf) > max {
			return "", errLineTooLong
		}

		buf = append(buf, b)
	}
}

// isCommand reports whether line opens with the given command word. RFC 5321 §2.4 makes commands
// case-insensitive, and the word ends at the first space or tab, so QUITTER is not QUIT.
func isCommand(line, verb string) bool {
	if len(line) < len(verb) || !strings.EqualFold(line[:len(verb)], verb) {
		return false
	}

	return len(line) == len(verb) || line[len(verb)] == ' ' || line[len(verb)] == '\t'
}

// ehloArgument reports whether line is an EHLO or HELO command carrying the one argument RFC 5321
// §4.1.1.1 requires. The argument is never read: §4.1.4 lets a server compare it with the peer
// address but forbids refusing a message when the comparison fails, so once the verb is known the
// name says nothing the server may act on, and demanding a particular one would be the only reply
// no real relay makes. Only its presence is checked, which is also the only thing a session needs.
func ehloArgument(line string) bool {
	f := strings.Fields(line)

	if len(f) != 2 {
		return false
	}

	return strings.EqualFold(f[0], "EHLO") || strings.EqualFold(f[0], "HELO")
}

// hasPrefixFold reports whether line begins with prefix, ignoring case. It is for the fixed part of
// a command line only, never for secret material: this one returns as soon as a byte differs, so
// the proof value must go through subtle.ConstantTimeCompare instead.
func hasPrefixFold(line, prefix string) bool {
	return len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix)
}

// readCommand reads one client line and answers the two cases every stage treats the same way: QUIT
// gets 221, and a malformed line gets 500. It returns errQuit once the 221 is on the wire. Any other
// read error keeps its own meaning, and the caller closes without a reply because there is nothing
// left that could be answered.
func readCommand(w io.Writer, r *bufio.Reader) (string, error) {
	line, err := readLine(r, maxLine)
	if err != nil {
		// Both malformed forms get the same 500: RFC 5321 §4.2.2 counts a command line that is too
		// long as a syntax error, and §4.1.1.4 forbids accepting a line that ends in a bare LF.
		// Staying silent would be a tell that the line was never a real command.
		if !errors.Is(err, errLineTooLong) && !errors.Is(err, errBareLF) {
			return "", err
		}

		if _, werr := io.WriteString(w, replySyntax+"\r\n"); werr != nil {
			return "", fmt.Errorf("write 500: %w", werr)
		}

		return "", err
	}

	if !isCommand(line, "QUIT") {
		return line, nil
	}

	if _, err := io.WriteString(w, replyBye+"\r\n"); err != nil {
		return "", fmt.Errorf("write 221: %w", err)
	}

	return "", errQuit
}

// reject sends one reply and returns cause, so a failed stage answers exactly once before the
// caller closes the connection.
func reject(w io.Writer, reply string, cause error) error {
	if _, err := io.WriteString(w, reply+"\r\n"); err != nil {
		return fmt.Errorf("write reply: %w", err)
	}

	return cause
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

// proofValue returns the client response for a nonce: the HMAC in unpadded base64. Both sides
// compute it, so it authenticates the client without sending the secret. The padding is left off
// because RFC 5321 §4.1.1.2 excludes "=" from an esmtp-value, and this value is one.
func proofValue(secret string, nonce []byte) string {
	return base64.RawStdEncoding.EncodeToString(hmacSum(secret, labelProof, nonce))
}

// mailCommand returns the MAIL FROM line that carries the proof. Both sides build it from the same
// constant, so the client's send and the server's expectation cannot drift apart.
func mailCommand(secret string, nonce []byte) string {
	return mailPrefix + proofValue(secret, nonce)
}

// deriveKeys returns the directional keys for a session.
func deriveKeys(secret string, nonce []byte) sessionKeys {
	var k sessionKeys

	copy(k.c2s[:], hmacSum(secret, labelC2S, nonce))
	copy(k.s2c[:], hmacSum(secret, labelS2C, nonce))

	return k
}
