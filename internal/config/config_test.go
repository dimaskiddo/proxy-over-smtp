package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"server", Config{Mode: "server", AuthSecret: "s"}, false},
		{"client", Config{Mode: "client", AuthSecret: "s"}, false},
		{"typo mode", Config{Mode: "clinet", AuthSecret: "s"}, true},
		{"empty mode", Config{AuthSecret: "s"}, true},
		{"empty secret", Config{Mode: "server"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
