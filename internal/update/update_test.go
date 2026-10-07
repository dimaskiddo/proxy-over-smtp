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

func TestShouldUpdate(t *testing.T) {
	const sha = "c04bfca9d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6"

	tests := []struct {
		name                    string
		latestTag, latestCommit string
		curTag, curCommit       string
		want, wantErr           bool
	}{
		{"newer tag", "v0.1.0", sha, "v0.0.3", sha, true, false},
		{"newer tag, unknown commits", "v0.1.0", "", "v0.0.3", "", true, false},
		{"same tag, different commit", "v0.0.3", sha, "v0.0.3", strings.Repeat("a", 40), true, false},
		{"same tag, same commit", "v0.0.3", sha, "v0.0.3", sha, false, false},
		{"same tag, short vs full commit", "v0.0.3", sha, "v0.0.3", sha[:7], false, false},
		{"same tag, commit case differs", "v0.0.3", strings.ToUpper(sha), "v0.0.3", sha, false, false},
		{"same tag, release commit unknown", "v0.0.3", "", "v0.0.3", sha, false, false},
		{"same tag, running commit unknown", "v0.0.3", sha, "v0.0.3", "none", false, false},
		{"same tag, both commits unknown", "v0.0.3", "none", "v0.0.3", "", false, false},
		{"same tag, running build dirty", "v0.0.3", sha, "v0.0.3-1-g09f0e68-dirty", sha[:7], false, false},
		{"same tag, commit too short to compare", "v0.0.3", "abc", "v0.0.3", "abd", false, false},
		{"older tag is not a downgrade", "v0.0.2", sha, "v0.0.3", strings.Repeat("b", 40), false, false},
		{"dev running build is refused", "v0.0.3", sha, "dev", "none", false, true},
		{"unparsable tag is refused", "nope", sha, "v0.0.3", sha, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShouldUpdate(tt.latestTag, tt.latestCommit, tt.curTag, tt.curCommit)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %v, %v; want %v, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestLatestResolvesCommit checks the tag is resolved to a commit when the API is the standard
// release endpoint, and that the lookup is simply skipped for any other shape.
func TestLatestResolvesCommit(t *testing.T) {
	const sha = "c04bfca9d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6"

	tests := []struct {
		name       string
		commitCode int
		want       string
	}{
		{"resolved", http.StatusOK, sha},
		{"lookup fails, tag only", http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			defer srv.Close()

			mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"tag_name":"v0.2.0","assets":[]}`)
			})
			mux.HandleFunc("/commits/v0.2.0", func(w http.ResponseWriter, _ *http.Request) {
				if tt.commitCode != http.StatusOK {
					w.WriteHeader(tt.commitCode)
					return
				}

				fmt.Fprintf(w, `{"sha":%q}`, sha)
			})

			rel, err := Client{API: srv.URL + "/releases/latest"}.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if rel.Tag != "v0.2.0" || rel.Commit != tt.want {
				t.Fatalf("tag, commit = %q, %q; want v0.2.0, %q", rel.Tag, rel.Commit, tt.want)
			}
		})
	}
}

// TestLatestCustomAPISkipsCommit checks a non-standard endpoint is not second-guessed into a
// commit lookup, so a mirror or a test server needs only to serve the release payload.
func TestLatestCustomAPISkipsCommit(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.2.0","assets":[]}`)
	})
	mux.HandleFunc("/commits/v0.2.0", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("commit lookup must not run for a custom API")
	})

	rel, err := Client{API: srv.URL + "/latest"}.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Commit != "" {
		t.Fatalf("commit = %q, want empty", rel.Commit)
	}
}

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

// makeZip returns a zip archive that holds one file called name.
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

// TestMoveAside checks a successful rename is reported as success, with and without a stale
// .old from a previous update: wrapping a nil error made every Windows replacement fail.
func TestMoveAside(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "proxy-over-smtp")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := moveAside(exe); err != nil {
		t.Fatalf("first moveAside = %v, want nil", err)
	}

	if _, err := os.Stat(exe + oldSuffix); err != nil {
		t.Fatalf("stat %s: %v", exe+oldSuffix, err)
	}

	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := moveAside(exe); err != nil {
		t.Fatalf("moveAside with a stale %s = %v, want nil", oldSuffix, err)
	}
}
