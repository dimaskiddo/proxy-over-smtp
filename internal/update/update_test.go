package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
		wantErr            bool
	}{
		{"linux", "386", "proxy-over-smtp_0.0.3_linux_32-bit.zip", false},
		{"linux", "amd64", "proxy-over-smtp_0.0.3_linux_64-bit.zip", false},
		{"linux", "arm64", "proxy-over-smtp_0.0.3_linux_arm-64-bit.zip", false},
		{"darwin", "386", "proxy-over-smtp_0.0.3_macos_32-bit.zip", false},
		{"darwin", "amd64", "proxy-over-smtp_0.0.3_macos_64-bit.zip", false},
		{"darwin", "arm64", "proxy-over-smtp_0.0.3_macos_arm-64-bit.zip", false},
		{"windows", "386", "proxy-over-smtp_0.0.3_windows_32-bit.zip", false},
		{"windows", "amd64", "proxy-over-smtp_0.0.3_windows_64-bit.zip", false},
		{"windows", "arm64", "proxy-over-smtp_0.0.3_windows_arm-64-bit.zip", false},
		{"freebsd", "amd64", "", true},
		{"linux", "riscv64", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			got, err := assetName("0.0.3", tt.goos, tt.goarch)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want, wantErr   bool
	}{
		{"v0.1.0", "v0.0.3", true, false},
		{"v0.0.4", "v0.0.3", true, false},
		{"v1.0.0", "v0.9.9", true, false},
		{"v0.0.3", "v0.0.3", false, false},
		{"v0.0.3", "v0.0.4", false, false},
		{"v0.0.3", "0.0.3", false, false},
		{"v0.0.4", "v0.0.3-1-g09f0e68-dirty", true, false},
		{"v0.0.3", "v0.0.3-1-g09f0e68-dirty", false, false},
		{"v0.0.3", "v0.0.3+build", false, false},
		{"v0.0.3", "dev", false, true},
		{"nope", "v0.0.3", false, true},
		{"v1.2", "v0.0.3", false, true},
		{"v1.x.0", "v0.0.3", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.latest+" vs "+tt.current, func(t *testing.T) {
			got, err := Newer(tt.latest, tt.current)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %v, %v; want %v, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, f := range []struct {
		name string
		data []byte
	}{{"LICENSE", []byte("mit")}, {name, content}} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// releaseServer serves a release JSON plus the asset and checksums for this platform.
func releaseServer(t *testing.T, tag string, archive []byte, sumOverride string) *httptest.Server {
	t.Helper()

	name, err := assetName(strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}

	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		hexSum = sumOverride
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
			tag, name, srv.URL+"/asset", srv.URL+"/sums")
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n%s  other.zip\n", hexSum, name, strings.Repeat("0", 64))
	})

	return srv
}

func TestApply(t *testing.T) {
	binName := binaryName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}

	newBin := []byte("new binary contents")
	archive := makeZip(t, binName, newBin)

	tests := []struct {
		name        string
		sumOverride string
		wantErr     string
		wantContent string
	}{
		{"valid", "", "", "new binary contents"},
		{"bad checksum", strings.Repeat("a", 64), "checksum mismatch", "old binary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := releaseServer(t, "v0.2.0", archive, tt.sumOverride)

			exe := filepath.Join(t.TempDir(), binName)
			if err := os.WriteFile(exe, []byte("old binary"), 0o750); err != nil {
				t.Fatal(err)
			}

			c := Client{API: srv.URL + "/latest"}

			rel, err := c.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if rel.Tag != "v0.2.0" {
				t.Fatalf("tag = %q", rel.Tag)
			}

			err = c.Apply(context.Background(), rel, exe)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != tt.wantContent {
				t.Fatalf("content = %q, want %q", got, tt.wantContent)
			}

			if runtime.GOOS != "windows" {
				info, err := os.Stat(exe)
				if err != nil {
					t.Fatal(err)
				}

				if info.Mode().Perm() != 0o750 {
					t.Fatalf("mode = %v, want 0750", info.Mode().Perm())
				}
			}

			entries, err := os.ReadDir(filepath.Dir(exe))
			if err != nil {
				t.Fatal(err)
			}

			for _, e := range entries {
				if strings.Contains(e.Name(), ".new-") {
					t.Fatalf("leftover temp file %s", e.Name())
				}
			}
		})
	}
}

func TestLatestErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{"rate limited", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, "rate limited"},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, "status 500"},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{")) }, "decode release"},
		{"no tag", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"assets":[]}`)) }, "no tag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			_, err := Client{API: srv.URL}.Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
