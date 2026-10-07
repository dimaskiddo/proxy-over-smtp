package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok", Config{Listen: ":1", Secret: "s"}, false},
		{"empty secret", Config{Listen: ":1"}, true},
		{"empty listen", Config{Secret: "s"}, true},
		{"tls pair", Config{Listen: ":1", Secret: "s", TLSCert: "c", TLSKey: "k"}, false},
		{"tls cert only", Config{Listen: ":1", Secret: "s", TLSCert: "c"}, true},
		{"tls key only", Config{Listen: ":1", Secret: "s", TLSKey: "k"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
