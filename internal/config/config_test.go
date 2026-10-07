package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok aes", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, false},
		{"ok xor", Config{Listen: ":1", Secret: "s", Cipher: CipherXOR, MaxStreams: DefaultMaxStreams}, false},
		{"empty secret", Config{Listen: ":1", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, true},
		{"empty cipher", Config{Listen: ":1", Secret: "s", MaxStreams: DefaultMaxStreams}, true},
		{"bad cipher", Config{Listen: ":1", Secret: "s", Cipher: "aes-256-gcm", MaxStreams: DefaultMaxStreams}, true},
		{"empty listen", Config{Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, true},
		{"bad listen no port", Config{Listen: "0.0.0.0", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, true},
		{"bad listen too many colons", Config{Listen: "0.0.0.0:80:90", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, true},
		{"ok remote", Config{Listen: ":1", Remote: "127.0.0.1:465", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, false},
		{"bad remote", Config{Listen: ":1", Remote: "127.0.0.1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, true},
		{"tls pair", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, TLSCert: "c", TLSKey: "k"}, false},
		{"tls cert only", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, TLSCert: "c"}, true},
		{"tls key only", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, TLSKey: "k"}, true},
		{"max streams zero", Config{Listen: ":1", Secret: "s", Cipher: CipherAES}, true},
		{"max streams negative", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: -1}, true},
		{"pool unset is ok", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams}, false},
		{"pool defaults ok", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: DefaultPoolMin, PoolMax: DefaultPoolMax}, false},
		{"pool equal ok", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: 3, PoolMax: 3}, false},
		{"pool at cap ok", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: 1, PoolMax: MaxPoolSize}, false},
		{"pool min above max", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: 9, PoolMax: 8}, true},
		{"pool min zero with max set", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMax: 8}, true},
		{"pool max zero with min set", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: 2}, true},
		{"pool min negative", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: -1, PoolMax: 8}, true},
		{"pool max above cap", Config{Listen: ":1", Secret: "s", Cipher: CipherAES, MaxStreams: DefaultMaxStreams, PoolMin: 2, PoolMax: MaxPoolSize + 1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
