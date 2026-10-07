// Package aesstream wraps an io.ReadWriter with AES-256-GCM record encryption. It provides
// confidentiality and integrity for the bytes it carries, and is the encrypted counterpart to
// the obfuscation-only pkg/xorstream.
package aesstream

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// maxRecord caps the plaintext sealed into one record. It bounds the transient ciphertext buffer
// to a size the relay's copy buffer covers in a whole number of records.
const maxRecord = 16 << 10

// hdrLen is the big-endian record length prefix that precedes every sealed record.
const hdrLen = 4

// Errors returned by Read when a peer's records cannot be processed.
var (
	// ErrRecordTooLarge is returned when a peer announces a record over the wire limit.
	ErrRecordTooLarge = errors.New("aesstream: record too large")

	// ErrAuth is returned when a record fails GCM authentication: tampering, a wrong key or a
	// lost stream position all look the same.
	ErrAuth = errors.New("aesstream: authentication failed")

	// ErrShortRecord is returned when the connection ends inside a record.
	ErrShortRecord = errors.New("aesstream: truncated record")
)

// Stream encrypts everything written and decrypts everything read with AES-256-GCM. Each
// direction has its own key and its own implicit nonce counter, so the two directions never
// share a nonce and no coordination is needed on the wire.
type Stream struct {
	inner io.ReadWriter

	// muW guards the write cipher and counter, muR the read side, so a full-duplex stream never
	// makes one direction wait for the other.
	muW  sync.Mutex
	seal cipher.AEAD
	wseq uint64
	wnb  []byte

	muR  sync.Mutex
	open cipher.AEAD
	rseq uint64
	rnb  []byte
	buf  []byte // decrypted bytes not yet handed to the caller
	rbuf []byte // reusable ciphertext buffer
	cbuf []byte // reusable ciphertext buffer for Write
}

// New returns a Stream that seals writes with writeKey and opens reads with readKey. Both keys
// must be 32 bytes for AES-256. Both ends swap the pair: what one writes with its write key the
// other reads with its read key.
func New(rw io.ReadWriter, writeKey, readKey []byte) (*Stream, error) {
	seal, err := gcm(writeKey)
	if err != nil {
		return nil, err
	}

	open, err := gcm(readKey)
	if err != nil {
		return nil, err
	}

	return &Stream{
		inner: rw,
		seal:  seal,
		open:  open,
		wnb:   make([]byte, seal.NonceSize()),
		rnb:   make([]byte, open.NonceSize()),
	}, nil
}

// gcm returns an AES-GCM AEAD for a 32-byte key.
func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return cipher.NewGCM(block)
}

// setNonce fills the last 8 bytes of an all-zero nonce with seq, big-endian. The counter is
// implicit and never sent, so TCP ordering alone keeps both sides in step.
func setNonce(nb []byte, seq uint64) []byte {
	clear(nb)
	binary.BigEndian.PutUint64(nb[len(nb)-8:], seq)

	return nb
}

// Read decrypts the next record and returns its plaintext, buffering the part that does not fit
// in p. It returns io.EOF only when the inner reader ends exactly on a record boundary.
func (s *Stream) Read(p []byte) (int, error) {
	s.muR.Lock()
	defer s.muR.Unlock()

	if len(s.buf) > 0 {
		n := copy(p, s.buf)
		s.buf = s.buf[n:]
		return n, nil
	}

	if len(p) == 0 {
		return 0, nil
	}

	// The header is a local, not a field: Read and Write hold different mutexes and would
	// otherwise clobber a shared buffer.
	var hdr [hdrLen]byte

	if _, err := io.ReadFull(s.inner, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}

		return 0, err
	}

	size := binary.BigEndian.Uint32(hdr[:])
	if size < uint32(s.open.Overhead()) || size > maxRecord+uint32(s.open.Overhead()) {
		return 0, ErrRecordTooLarge
	}

	if cap(s.rbuf) < int(size) {
		s.rbuf = make([]byte, size)
	}

	rec := s.rbuf[:size]
	if _, err := io.ReadFull(s.inner, rec); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, ErrShortRecord
		}

		return 0, err
	}

	// GCM opens in place here: the output starts at the same address as the ciphertext.
	plain, err := s.open.Open(rec[:0], setNonce(s.rnb, s.rseq), rec, nil)
	if err != nil {
		return 0, ErrAuth
	}

	s.rseq++

	n := copy(p, plain)
	s.buf = append(s.buf[:0], plain[n:]...)

	return n, nil
}

// Write seals p into one or more records and writes them to the inner stream. The caller's slice
// is never modified.
func (s *Stream) Write(p []byte) (int, error) {
	s.muW.Lock()
	defer s.muW.Unlock()

	total := 0

	for len(p) > 0 {
		n := min(len(p), maxRecord)

		if err := s.writeRecord(p[:n]); err != nil {
			return total, err
		}

		p = p[n:]
		total += n
	}

	return total, nil
}

// writeRecord seals one chunk of at most maxRecord bytes and writes its length prefix followed by
// the ciphertext in a single write. The counter advances only once the bytes are on the wire, so it
// always equals the number of records the peer can have seen.
func (s *Stream) writeRecord(p []byte) error {
	need := hdrLen + len(p) + s.seal.Overhead()
	if cap(s.cbuf) < need {
		s.cbuf = make([]byte, need)
	}

	// One buffer holds the length prefix and the sealed record, so a record costs one syscall
	// instead of two with the header and the ciphertext separate.
	rec := s.seal.Seal(s.cbuf[hdrLen:hdrLen], setNonce(s.wnb, s.wseq), p, nil)
	binary.BigEndian.PutUint32(s.cbuf[:hdrLen], uint32(len(rec)))

	if err := writeFull(s.inner, s.cbuf[:hdrLen+len(rec)]); err != nil {
		return err
	}

	s.wseq++

	return nil
}

// writeFull writes p in full. An io.Writer may accept a short write without an error, so a
// single Write call is not enough.
func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}

		if n == 0 {
			return io.ErrShortWrite
		}

		p = p[n:]
	}

	return nil
}

// Close closes the inner stream when it is an io.Closer.
func (s *Stream) Close() error {
	if closer, ok := s.inner.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}
