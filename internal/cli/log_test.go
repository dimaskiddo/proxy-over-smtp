package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name, level, format string
		wantErr             bool
		wantOut             string
	}{
		{"text info", "info", "text", false, "msg=hello"},
		{"json info", "info", "json", false, `"msg":"hello"`},
		{"bad level", "loud", "text", true, ""},
		{"bad format", "info", "xml", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			l, c, err := newLogger(tt.level, tt.format, "", &buf)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if err != nil {
				return
			}
			defer c.Close()

			l.Info("hello")
			l.Debug("hidden")

			if !strings.Contains(buf.String(), tt.wantOut) || strings.Contains(buf.String(), "hidden") {
				t.Fatalf("output %q", buf.String())
			}
		})
	}
}
