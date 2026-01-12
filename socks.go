package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// --- SOCKS5 Implementation ---
func negotiateSocks5Server(rw io.ReadWriter) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(rw, header); err != nil {
		return "", err
	}

	if header[0] != 0x05 {
		return "", fmt.Errorf("Wrong SOCKS version. Please use SOCKS version 5")
	}

	methods := make([]byte, int(header[1]))

	io.ReadFull(rw, methods)
	rw.Write([]byte{0x05, 0x00})

	req := make([]byte, 4)
	io.ReadFull(rw, req)

	var host string
	switch req[3] {
	case 0x01:
		ip := make([]byte, 4)
		io.ReadFull(rw, ip)

		host = net.IP(ip).String()

	case 0x03:
		lBuf := make([]byte, 1)
		io.ReadFull(rw, lBuf)

		domain := make([]byte, int(lBuf[0]))

		io.ReadFull(rw, domain)
		host = string(domain)

	default:
		return "", fmt.Errorf("Unsupported ATYP")
	}

	pBuf := make([]byte, 2)
	io.ReadFull(rw, pBuf)

	port := binary.BigEndian.Uint16(pBuf)

	rw.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return fmt.Sprintf("%s:%d", host, port), nil
}
