package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// DefaultAPI is the GitHub latest-release endpoint.
const DefaultAPI = "https://api.github.com/repos/dimaskiddo/proxy-over-smtp/releases/latest"

const (
	binaryName    = "proxy-over-smtp"
	checksumsName = "checksums.txt"
	maxDownload   = 100 << 20
)

// Release is a published release: its tag and asset name to download URL.
type Release struct {
	Tag    string
	Assets map[string]string
}

// Client talks to the release API. A nil HTTP uses http.DefaultClient.
type Client struct {
	HTTP *http.Client
	API  string
}

func (c Client) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("User-Agent", binaryName)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("request %s: status %d (rate limited?)", url, resp.StatusCode)
		}

		return nil, fmt.Errorf("request %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}

	if int64(len(body)) > limit {
		return nil, fmt.Errorf("read %s: response exceeds %d bytes", url, limit)
	}

	return body, nil
}

// Latest fetches the newest published release.
func (c Client) Latest(ctx context.Context) (Release, error) {
	body, err := c.get(ctx, c.API, "application/vnd.github+json", 1<<20)
	if err != nil {
		return Release{}, err
	}

	var raw struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Release{}, fmt.Errorf("decode release: %w", err)
	}

	if raw.Tag == "" {
		return Release{}, errors.New("release has no tag")
	}

	rel := Release{Tag: raw.Tag, Assets: make(map[string]string, len(raw.Assets))}
	for _, a := range raw.Assets {
		rel.Assets[a.Name] = a.URL
	}

	return rel, nil
}

// Apply downloads the asset for this platform, verifies its sha256 and replaces exe.
func (c Client) Apply(ctx context.Context, rel Release, exe string) error {
	name, err := assetName(strings.TrimPrefix(rel.Tag, "v"), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	assetURL, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("release %s has no asset %s", rel.Tag, name)
	}

	sumsURL, ok := rel.Assets[checksumsName]
	if !ok {
		return fmt.Errorf("release %s has no %s", rel.Tag, checksumsName)
	}

	sums, err := c.get(ctx, sumsURL, "", 1<<20)
	if err != nil {
		return err
	}

	archive, err := c.get(ctx, assetURL, "", maxDownload)
	if err != nil {
		return err
	}

	if err := verify(sums, name, archive); err != nil {
		return err
	}

	bin, err := extract(archive)
	if err != nil {
		return err
	}

	cleanOld(exe)

	return replace(exe, bin)
}

func verify(sums []byte, name string, data []byte) error {
	var want string
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			want = f[0]
			break
		}
	}

	if want == "" {
		return fmt.Errorf("no checksum for %s", name)
	}

	got := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return fmt.Errorf("checksum mismatch for %s", name)
	}

	return nil
}

func extract(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}

	for _, f := range zr.File {
		base := path.Base(f.Name)
		if base != binaryName && base != binaryName+".exe" {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}

		bin, err := io.ReadAll(io.LimitReader(rc, maxDownload+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}

		if len(bin) > maxDownload {
			return nil, fmt.Errorf("binary %s exceeds %d bytes", f.Name, maxDownload)
		}

		return bin, nil
	}

	return nil, errors.New("archive has no binary")
}

// assetName mirrors the archive naming in .goreleaser.yml. Change both together.
func assetName(version, goos, goarch string) (string, error) {
	osNames := map[string]string{"linux": "linux", "darwin": "macos", "windows": "windows"}
	archNames := map[string]string{"386": "32-bit", "amd64": "64-bit", "arm64": "arm-64-bit"}

	o, ok := osNames[goos]
	if !ok {
		return "", fmt.Errorf("unsupported os %s", goos)
	}

	a, ok := archNames[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported arch %s", goarch)
	}

	return fmt.Sprintf("%s_%s_%s_%s.zip", binaryName, version, o, a), nil
}

// Newer reports whether latest is a higher X.Y.Z than current. Any "-pre" or "+meta" suffix is ignored,
// so git-describe builds compare by their base tag.
// ponytail: pre-release ordering is not handled; switch to x/mod/semver if it is needed.
func Newer(latest, current string) (bool, error) {
	l, err := parseVersion(latest)
	if err != nil {
		return false, err
	}

	c, err := parseVersion(current)
	if err != nil {
		return false, err
	}

	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i], nil
		}
	}

	return false, nil
}

func parseVersion(v string) ([3]int, error) {
	var out [3]int

	base := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(base, "-+"); i >= 0 {
		base = base[:i]
	}

	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("invalid version %q", v)
	}

	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("invalid version %q", v)
		}

		out[i] = n
	}

	return out, nil
}
