package socks4

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestReadRequest(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr bool
	}{
		{"ipv4", []byte{4, 1, 0x01, 0xBB, 93, 184, 216, 34, 0}, "93.184.216.34:443", false},
		{"ipv4 with user id", append([]byte{4, 1, 0, 80, 10, 0, 0, 1}, []byte("bob\x00")...), "10.0.0.1:80", false},
		{"4a domain", append([]byte{4, 1, 0x01, 0xBB, 0, 0, 0, 1, 0}, []byte("example.com\x00")...), "example.com:443", false},
		{"4a empty domain", []byte{4, 1, 0, 80, 0, 0, 0, 1, 0, 0}, "", true},
		{"bind rejected", []byte{4, 2, 0, 80, 1, 2, 3, 4, 0}, "", true},
		{"wrong version", []byte{5, 1, 0, 80, 1, 2, 3, 4, 0}, "", true},
		{"truncated", []byte{4, 1, 0}, "", true},
		{"user id too long", append([]byte{4, 1, 0, 80, 1, 2, 3, 4}, []byte(strings.Repeat("a", 300)+"\x00")...), "", true},
		{"domain too long", append([]byte{4, 1, 0, 80, 0, 0, 0, 1, 0}, []byte(strings.Repeat("a", 300)+"\x00")...), "", true},
		{"domain unterminated", append([]byte{4, 1, 0, 80, 0, 0, 0, 1, 0}, []byte("abc")...), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadRequest(bufio.NewReader(bytes.NewReader(tt.in)))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestWriteReply(t *testing.T) {
	tests := []struct {
		granted bool
		want    []byte
	}{
		{true, []byte{0, 0x5A, 0, 0, 0, 0, 0, 0}},
		{false, []byte{0, 0x5B, 0, 0, 0, 0, 0, 0}},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		if err := WriteReply(&buf, tt.granted); err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(buf.Bytes(), tt.want) {
			t.Fatalf("granted=%v got % x, want % x", tt.granted, buf.Bytes(), tt.want)
		}
	}
}
