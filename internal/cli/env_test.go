package cli

import (
	"testing"

	"github.com/spf13/pflag"
)

func TestEnvName(t *testing.T) {
	tests := map[string]string{
		"secret":        "PROXY_OVER_SMTP_SECRET",
		"allow-private": "PROXY_OVER_SMTP_ALLOW_PRIVATE",
		"log-level":     "PROXY_OVER_SMTP_LOG_LEVEL",
	}

	for in, want := range tests {
		if got := envName(in); got != want {
			t.Errorf("envName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyEnv(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		want     string
		wantBool bool
		wantErr  bool
	}{
		{"default", nil, nil, "def", false, false},
		{"env over default", nil, map[string]string{"PROXY_OVER_SMTP_SECRET": "fromenv"}, "fromenv", false, false},
		{"flag over env", []string{"--secret", "fromflag"}, map[string]string{"PROXY_OVER_SMTP_SECRET": "fromenv"}, "fromflag", false, false},
		{"bool env", nil, map[string]string{"PROXY_OVER_SMTP_ALLOW_PRIVATE": "true"}, "def", true, false},
		{"bad bool env", nil, map[string]string{"PROXY_OVER_SMTP_ALLOW_PRIVATE": "nope"}, "", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			var secret string
			var priv bool
			fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
			fs.StringVar(&secret, "secret", "def", "")
			fs.BoolVar(&priv, "allow-private", false, "")

			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}

			err := applyEnv(fs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && (secret != tt.want || priv != tt.wantBool) {
				t.Fatalf("secret=%q priv=%v, want %q %v", secret, priv, tt.want, tt.wantBool)
			}
		})
	}
}
