package cli

import (
	"bytes"
	"strings"
	"testing"
)

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
		{"unknown flag", []string{"server", "--nope"}, nil, "unknown flag", ""},
		{"old style flag", []string{"server", "-secret", "x"}, nil, "unknown shorthand", ""},
		{"bad env bool", []string{"server"}, map[string]string{"PROXY_OVER_SMTP_ALLOW_PRIVATE": "x"}, "PROXY_OVER_SMTP_ALLOW_PRIVATE", ""},
		{"bad log level", []string{"--log-level", "loud", "server", "--secret", "x"}, nil, "invalid log level", ""},
		{"help lists env", []string{"server", "--help"}, nil, "", "$PROXY_OVER_SMTP_SECRET"},
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
