package socks5

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// Reply codes from RFC 1928.
const (
	Succeeded            byte = 0x00
	GeneralFailure       byte = 0x01
	NotAllowed           byte = 0x02
	NetworkUnreachable   byte = 0x03
	HostUnreachable      byte = 0x04
	ConnectionRefused    byte = 0x05
	CommandNotSupported  byte = 0x07
	AddrTypeNotSupported byte = 0x08
)

const (
	version   = 0x05
	noAuth    = 0x00
	noMethods = 0xFF
	cmdCon    = 0x01
	atypIPv4  = 0x01
	atypName  = 0x03
	atypIPv6  = 0x04
)

// ReadRequest runs the greeting and reads a CONNECT request.
// The caller must answer with WriteReply once the target outcome is known.
func ReadRequest(rw io.ReadWriter) (string, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(rw, head); err != nil {
		return "", fmt.Errorf("read greeting: %w", err)
	}

	if head[0] != version {
		return "", fmt.Errorf("unsupported socks version %d", head[0])
	}

	methods := make([]byte, head[1])
	if _, err := io.ReadFull(rw, methods); err != nil {
		return "", fmt.Errorf("read methods: %w", err)
	}

	offered := false
	for _, m := range methods {
		if m == noAuth {
			offered = true
			break
		}
	}

	if !offered {
		if _, err := rw.Write([]byte{version, noMethods}); err != nil {
			return "", fmt.Errorf("write method reply: %w", err)
		}

		return "", errors.New("no acceptable auth method")
	}

	if _, err := rw.Write([]byte{version, noAuth}); err != nil {
		return "", fmt.Errorf("write method reply: %w", err)
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(rw, req); err != nil {
		return "", fmt.Errorf("read request: %w", err)
	}

	if req[0] != version {
		return "", fmt.Errorf("unsupported socks version %d", req[0])
	}

	if req[1] != cmdCon {
		_ = WriteReply(rw, CommandNotSupported, nil)
		return "", fmt.Errorf("unsupported command %d", req[1])
	}

	var host string
	switch req[3] {
	case atypIPv4:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(rw, ip); err != nil {
			return "", fmt.Errorf("read ipv4: %w", err)
		}

		host = net.IP(ip).String()

	case atypIPv6:
		ip := make([]byte, 16)
		if _, err := io.ReadFull(rw, ip); err != nil {
			return "", fmt.Errorf("read ipv6: %w", err)
		}

		host = net.IP(ip).String()

	case atypName:
		l := make([]byte, 1)
		if _, err := io.ReadFull(rw, l); err != nil {
			return "", fmt.Errorf("read domain length: %w", err)
		}

		if l[0] == 0 {
			_ = WriteReply(rw, GeneralFailure, nil)
			return "", errors.New("empty domain")
		}

		name := make([]byte, l[0])
		if _, err := io.ReadFull(rw, name); err != nil {
			return "", fmt.Errorf("read domain: %w", err)
		}

		host = string(name)

	default:
		_ = WriteReply(rw, AddrTypeNotSupported, nil)
		return "", fmt.Errorf("unsupported address type %d", req[3])
	}

	p := make([]byte, 2)
	if _, err := io.ReadFull(rw, p); err != nil {
		return "", fmt.Errorf("read port: %w", err)
	}

	return net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(p))), nil
}

// WriteReply sends a reply with the bound address, or zero IPv4 when bound is not a *net.TCPAddr.
func WriteReply(w io.Writer, code byte, bound net.Addr) error {
	reply := []byte{version, code, 0x00}

	var ip net.IP
	var port int
	if a, ok := bound.(*net.TCPAddr); ok && a != nil {
		ip, port = a.IP, a.Port
	}

	switch {
	case ip.To4() != nil:
		reply = append(reply, atypIPv4)
		reply = append(reply, ip.To4()...)
	case len(ip) == net.IPv6len:
		reply = append(reply, atypIPv6)
		reply = append(reply, ip...)
	default:
		reply = append(reply, atypIPv4, 0, 0, 0, 0)
	}

	reply = binary.BigEndian.AppendUint16(reply, uint16(port))

	if _, err := w.Write(reply); err != nil {
		return fmt.Errorf("write reply: %w", err)
	}

	return nil
}
