// Package socks4 implements the server side of the SOCKS4 and SOCKS4a CONNECT handshake.
package socks4

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// Reply codes from the SOCKS4 specification. WriteReply is the only writer, so they stay
// unexported and callers pass a bool.
const (
	replyGranted  byte = 0x5A
	replyRejected byte = 0x5B
)

// Protocol bytes from the SOCKS4 specification.
const (
	version    = 0x04
	cmdConnect = 0x01
	// maxField caps the user ID and the SOCKS4a domain so a peer cannot stream bytes forever.
	maxField = 255
)

// ReadRequest reads a CONNECT request and returns the target as host:port. The user ID is
// read and ignored. SOCKS4a requests, marked by a destination of 0.0.0.x with x != 0, carry
// the host name after the user ID. The caller must answer with WriteReply.
func ReadRequest(r *bufio.Reader) (string, error) {
	head := make([]byte, 8)
	if _, err := io.ReadFull(r, head); err != nil {
		return "", fmt.Errorf("read request: %w", err)
	}

	if head[0] != version {
		return "", fmt.Errorf("unsupported socks version %d", head[0])
	}

	if head[1] != cmdConnect {
		return "", fmt.Errorf("unsupported command %d", head[1])
	}

	if _, err := readCString(r); err != nil {
		return "", fmt.Errorf("read user id: %w", err)
	}

	port := fmt.Sprint(binary.BigEndian.Uint16(head[2:4]))
	ip := net.IP(head[4:8])

	// 0.0.0.x with x != 0 is the SOCKS4a marker: the host name follows the user ID.
	if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
		host, err := readCString(r)
		if err != nil {
			return "", fmt.Errorf("read domain: %w", err)
		}

		if host == "" {
			return "", errors.New("empty domain")
		}

		return net.JoinHostPort(host, port), nil
	}

	return net.JoinHostPort(ip.String(), port), nil
}

// WriteReply sends the 8-byte reply. The bound address is always zero because clients ignore
// it for CONNECT.
func WriteReply(w io.Writer, granted bool) error {
	code := replyRejected
	if granted {
		code = replyGranted
	}

	if _, err := w.Write([]byte{0x00, code, 0, 0, 0, 0, 0, 0}); err != nil {
		return fmt.Errorf("write reply: %w", err)
	}

	return nil
}

// readCString reads up to maxField bytes before a NUL, so a peer cannot grow memory without
// bound.
func readCString(r *bufio.Reader) (string, error) {
	var buf []byte

	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}

		if b == 0 {
			return string(buf), nil
		}

		if len(buf) >= maxField {
			return "", errors.New("field too long")
		}

		buf = append(buf, b)
	}
}
