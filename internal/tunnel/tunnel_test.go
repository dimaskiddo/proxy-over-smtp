package tunnel

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/netip"
	"testing"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
)

func TestHandshake(t *testing.T) {
	tests := []struct {
		name         string
		clientSecret string
		wantErr      bool
	}{
		{"match", "s3cret", false},
		{"wrong", "other", true},
		{"substring", "xx" + "s3cret" + "xx", true},
		{"prefix only", "s3cre", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(config.Config{AuthSecret: "s3cret"}, log.New(io.Discard, "", 0))
			cli := New(config.Config{AuthSecret: tt.clientSecret}, log.New(io.Discard, "", 0))

			c, s := net.Pipe()
			defer c.Close()
			defer s.Close()

			srvErr := make(chan error, 1)
			go func() {
				err := srv.serverHandshake(s, bufio.NewReader(s))
				if err != nil {
					s.Close()
				}
				srvErr <- err
			}()

			cliErr := cli.clientHandshake(c, bufio.NewReader(c))
			if (cliErr != nil) != tt.wantErr {
				t.Fatalf("client err = %v, wantErr %v", cliErr, tt.wantErr)
			}

			if err := <-srvErr; (err != nil) != tt.wantErr {
				t.Fatalf("server err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsBlocked(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"192.168.0.1", true},
		{"172.16.0.1", true},
		{"169.254.169.254", true},
		{"0.0.0.0", true},
		{"::ffff:127.0.0.1", true},
		{"8.8.8.8", false},
		{"2606:4700::1111", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := isBlocked(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Fatalf("isBlocked(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
