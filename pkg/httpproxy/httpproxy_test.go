package httpproxy

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadRequest(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantTarget string
		wantErr    error
		wantAnyErr bool
	}{
		{"connect with port", "CONNECT example.com:8443 HTTP/1.1\r\nHost: example.com:8443\r\n\r\n", "example.com:8443", nil, false},
		{"connect default port", "CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com:443", nil, false},
		{"connect ipv6 default port", "CONNECT [2001:db8::1] HTTP/1.1\r\nHost: x\r\n\r\n", "[2001:db8::1]:443", nil, false},
		{"absolute get", "GET http://example.com/a?b=1 HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com:80", nil, false},
		{"absolute with port", "GET http://example.com:8080/ HTTP/1.1\r\nHost: example.com:8080\r\n\r\n", "example.com:8080", nil, false},
		{"origin form", "GET /index.html HTTP/1.1\r\nHost: example.com\r\n\r\n", "", ErrNotProxy, false},
		{"ftp scheme", "GET ftp://example.com/x HTTP/1.1\r\nHost: example.com\r\n\r\n", "", ErrNotProxy, false},
		{"https scheme absolute", "GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", "", ErrNotProxy, false},
		{"garbage", "NOT HTTP\r\n\r\n", "", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, target, err := ReadRequest(bufio.NewReader(strings.NewReader(tt.in)))

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("want error")
				}
			case err != nil:
				t.Fatal(err)
			}

			if target != tt.wantTarget {
				t.Fatalf("target = %q, want %q", target, tt.wantTarget)
			}
		})
	}
}

func TestForward(t *testing.T) {
	in := "POST http://example.com/p?q=1 HTTP/1.1\r\nHost: example.com\r\nProxy-Connection: keep-alive\r\n" +
		"Proxy-Authorization: Basic eA==\r\nConnection: keep-alive, X-Drop\r\nX-Drop: 1\r\nX-Keep: 2\r\n" +
		"User-Agent: curl/8\r\nContent-Length: 5\r\n\r\nhello"

	req, _, err := ReadRequest(bufio.NewReader(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Forward(&out, req); err != nil {
		t.Fatal(err)
	}

	got := out.String()

	if !strings.HasPrefix(got, "POST /p?q=1 HTTP/1.1\r\n") {
		t.Fatalf("request line not origin form: %q", got)
	}

	for _, want := range []string{"Host: example.com", "X-Keep: 2", "User-Agent: curl/8", "Connection: close", "\r\n\r\nhello"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}

	for _, banned := range []string{"Proxy-Connection", "Proxy-Authorization", "X-Drop", "keep-alive"} {
		if strings.Contains(got, banned) {
			t.Errorf("unexpected %q in %q", banned, got)
		}
	}
}

func TestWriteStatus(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{200, "HTTP/1.1 200 Connection established\r\n\r\n"},
		{403, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"},
		{502, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		if err := WriteStatus(&buf, tt.code); err != nil {
			t.Fatal(err)
		}

		if buf.String() != tt.want {
			t.Fatalf("code %d: got %q, want %q", tt.code, buf.String(), tt.want)
		}
	}
}
