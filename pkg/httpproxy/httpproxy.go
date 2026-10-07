// Package httpproxy implements the server side of an HTTP forward proxy: CONNECT tunnels and
// absolute-form plain HTTP requests.
package httpproxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// errNotProxy marks a request that is not a proxy request: an origin-form request aimed at the
// proxy itself, or a scheme other than http. It stays unexported because the caller answers
// every parse failure with the same status.
var errNotProxy = errors.New("not a proxy request")

// hopByHop lists headers that apply to a single connection and must not reach the target.
var hopByHop = []string{
	"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade",
}

// ReadRequest reads one request and returns it with the target as host:port. CONNECT defaults
// to port 443 and absolute-form http URLs to port 80. The caller decides between a tunnel and
// Forward by checking req.Method.
func ReadRequest(br *bufio.Reader) (*http.Request, string, error) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, "", fmt.Errorf("read request: %w", err)
	}

	if req.Method == http.MethodConnect {
		if req.URL.Host == "" {
			return nil, "", errNotProxy
		}

		return req, withPort(req.URL.Host, "443"), nil
	}

	if !req.URL.IsAbs() || req.URL.Scheme != "http" || req.URL.Host == "" {
		return nil, "", errNotProxy
	}

	return req, withPort(req.URL.Host, "80"), nil
}

// WriteStatus writes a bodyless status response. Status 200 is the CONNECT success line.
func WriteStatus(w io.Writer, code int) error {
	line := "HTTP/1.1 200 Connection established\r\n\r\n"
	if code != http.StatusOK {
		line = fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, http.StatusText(code))
	}

	if _, err := io.WriteString(w, line); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	return nil
}

// Forward writes req to dst in origin form without hop-by-hop headers and with
// "Connection: close", so the target ends the exchange and the proxied connection
// serves exactly one request.
func Forward(dst io.Writer, req *http.Request) error {
	for _, v := range req.Header.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			req.Header.Del(strings.TrimSpace(name))
		}
	}

	for _, name := range hopByHop {
		req.Header.Del(name)
	}

	// An empty value stops net/http from injecting its own User-Agent.
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header["User-Agent"] = []string{""}
	}

	req.RequestURI = ""
	req.Close = true

	if err := req.Write(dst); err != nil {
		return fmt.Errorf("forward request: %w", err)
	}

	return nil
}

// withPort appends def when hostport has no port. One bracket pair around a bare IPv6 literal is
// removed first, so the result joins back into a valid host:port.
func withPort(hostport, def string) string {
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		return hostport
	}

	host := strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")

	return net.JoinHostPort(host, def)
}
