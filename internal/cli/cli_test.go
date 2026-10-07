package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newRoot builds the command tree for tests that do not need the app handle.
func newRoot(info BuildInfo, out io.Writer) *cobra.Command {
	root, _ := newApp(info, out)
	return root
}

// run executes the command tree with args and returns its combined stdout and stderr.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := newRoot(BuildInfo{Version: "v9.9.9", Commit: "abc1234", Date: "today"}, &out)
	root.SetArgs(args)
	root.SetErr(&out)

	err := root.Execute()
	return out.String(), err
}

func TestCommands(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
		wantOut string
	}{
		{"version cmd", []string{"version"}, nil, "", "v9.9.9"},
		{"version cmd author", []string{"version"}, nil, "", "By Dimas Restu H <drh.dimasrestu@gmail.com>"},
		{"version flag", []string{"--version"}, nil, "", "v9.9.9"},
		{"server no secret", []string{"server"}, nil, "secret is required", ""},
		{"client no secret", []string{"client"}, nil, "secret is required", ""},
		{"bad cipher", []string{"server", "--secret", "x", "--cipher", "aes-256-gcm"}, nil, "cipher must be xor or aes", ""},
		{"help lists cipher", []string{"server", "--help"}, nil, "", "xor or aes"},
		{"unknown flag", []string{"server", "--nope"}, nil, "unknown flag", ""},
		{"old style flag", []string{"server", "-secret", "x"}, nil, "unknown shorthand", ""},
		{"bad env bool", []string{"server"}, map[string]string{"PROXY_OVER_SMTP_ALLOW_PRIVATE": "x"}, "PROXY_OVER_SMTP_ALLOW_PRIVATE", ""},
		{"bad log level", []string{"--log-level", "loud", "server", "--secret", "x"}, nil, "invalid log level", ""},
		{"auto-update short interval", []string{"server", "--secret", "x", "--auto-update", "--update-interval", "10m"}, nil, "at least 1h0m0s", ""},
		{"tls cert without key", []string{"client", "--secret", "x", "--tls-cert", "c.pem"}, nil, "set together", ""},
		{"negative drain", []string{"server", "--secret", "x", "--drain-timeout", "-1s"}, nil, "must not be negative", ""},
		{"bad tls files", []string{"client", "--secret", "x", "--tls-cert", "/nope/c.pem", "--tls-key", "/nope/k.pem"}, nil, "load tls key pair", ""},
		{"update unreachable api", []string{"update", "--check", "--update-api", "http://127.0.0.1:1/x"}, nil, "check latest release", ""},
		{"max streams zero", []string{"server", "--secret", "x", "--max-streams", "0"}, nil, "max-streams must be greater than zero", ""},
		{"help lists max streams", []string{"server", "--help"}, nil, "", "$PROXY_OVER_SMTP_MAX_STREAMS"},
		{"help lists env", []string{"server", "--help"}, nil, "", "$PROXY_OVER_SMTP_SECRET"},
		{"help lists persistent env", []string{"server", "--help"}, nil, "", "$PROXY_OVER_SMTP_LOG_LEVEL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PROXY_OVER_SMTP_SECRET", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			out, err := run(t, tt.args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(out, tt.wantOut) {
				t.Fatalf("output %q missing %q", out, tt.wantOut)
			}
		})
	}
}

func TestUpdateDevBuild(t *testing.T) {
	var out bytes.Buffer
	root := newRoot(BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}, &out)
	root.SetArgs([]string{"update", "--update-api", "http://127.0.0.1:1/x"})
	root.SetErr(&out)

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "use --force") {
		t.Fatalf("err = %v, want dev build refusal", err)
	}
}
