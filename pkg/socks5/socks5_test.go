package socks5

import (
	"bytes"
	"net"
	"testing"
)

// rw reads the request from an in-memory reader and records everything written back.
type rw struct {
	*bytes.Reader
	out bytes.Buffer
}

// Write records p so tests can inspect the reply.
func (r *rw) Write(p []byte) (int, error) { return r.out.Write(p) }

// selectNoAuth is the method-selection reply for a greeting that offers no-auth.
var selectNoAuth = []byte{5, 0}

// failReply is the full failure reply written when a request is rejected after the greeting.
var failReply = []byte{5, GeneralFailure, 0, 1, 0, 0, 0, 0, 0, 0}

func TestReadRequest(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr bool
		// wantOut is the exact byte stream ReadRequest wrote back, which is what the peer sees.
		wantOut []byte
	}{
		{"ipv4", []byte{5, 1, 0, 5, 1, 0, 1, 1, 2, 3, 4, 0, 80}, "1.2.3.4:80", false, selectNoAuth},
		{"ipv6", append([]byte{5, 1, 0, 5, 1, 0, 4}, append(net.ParseIP("::1"), 0x01, 0xbb)...), "[::1]:443", false, selectNoAuth},
		{"domain", append([]byte{5, 1, 0, 5, 1, 0, 3, 3}, []byte("a.b\x00\x50")...), "a.b:80", false, selectNoAuth},
		{"bad version", []byte{4, 1, 0}, "", true, nil},
		{"no-auth not offered", []byte{5, 1, 2}, "", true, []byte{5, 0xFF}},
		{"bind cmd", []byte{5, 1, 0, 5, 2, 0, 1, 1, 2, 3, 4, 0, 80}, "", true, append(selectNoAuth, 5, CommandNotSupported, 0, 1, 0, 0, 0, 0, 0, 0)},
		{"bad atyp", []byte{5, 1, 0, 5, 1, 0, 9}, "", true, append(selectNoAuth, 5, AddrTypeNotSupported, 0, 1, 0, 0, 0, 0, 0, 0)},
		{"empty domain", []byte{5, 1, 0, 5, 1, 0, 3, 0}, "", true, append(selectNoAuth, failReply...)},
		{"bad version in request", []byte{5, 1, 0, 4, 1, 0, 1}, "", true, append(selectNoAuth, failReply...)},
		{"truncated greeting", []byte{5}, "", true, nil},
		{"truncated methods", []byte{5, 3, 0}, "", true, failReply},
		{"truncated request", []byte{5, 1, 0, 5, 1}, "", true, append(selectNoAuth, failReply...)},
		{"truncated ipv4", []byte{5, 1, 0, 5, 1, 0, 1, 1, 2}, "", true, append(selectNoAuth, failReply...)},
		{"truncated domain", []byte{5, 1, 0, 5, 1, 0, 3, 3, 'a'}, "", true, append(selectNoAuth, failReply...)},
		{"truncated port", []byte{5, 1, 0, 5, 1, 0, 1, 1, 2, 3, 4, 0}, "", true, append(selectNoAuth, failReply...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &rw{Reader: bytes.NewReader(tt.in)}
			got, err := ReadRequest(c)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}

			if !bytes.Equal(c.out.Bytes(), tt.wantOut) {
				t.Fatalf("reply %v, want %v", c.out.Bytes(), tt.wantOut)
			}
		})
	}
}

func TestWriteReply(t *testing.T) {
	tests := []struct {
		name  string
		bound net.Addr
		want  []byte
	}{
		{"nil", nil, []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}},
		{"v4", &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 258}, []byte{5, 0, 0, 1, 1, 2, 3, 4, 1, 2}},
		{"v6", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 1}, append(append([]byte{5, 0, 0, 4}, net.ParseIP("::1")...), 0, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := WriteReply(&b, Succeeded, tt.bound); err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(b.Bytes(), tt.want) {
				t.Fatalf("got %v want %v", b.Bytes(), tt.want)
			}
		})
	}
}
