// Package update finds the latest GitHub release, downloads the archive for the running
// platform, verifies its checksum and replaces the executable. Archive names are coupled to
// .goreleaser.yml through assetName.
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
	"net/url"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is the GitHub latest-release endpoint.
const DefaultAPI = "https://api.github.com/repos/dimaskiddo/proxy-over-smtp/releases/latest"
const (
	// binaryName is the archive prefix and the executable name inside it. It must match the
	// GoReleaser project name.
	binaryName = "proxy-over-smtp"
	// checksumsName is the release asset that lists the sha256 of every archive.
	checksumsName = "checksums.txt"
	// maxDownload caps memory use, because the archive is held in memory while it is verified.
	maxDownload = 100 << 20
	// latestSuffix is the part of DefaultAPI that the release base URL is derived from, so the
	// tag-to-commit lookup can be built from a custom --update-api too.
	latestSuffix = "/releases/latest"
	// minCommit is the shortest commit prefix treated as meaningful. Git's own abbreviation
	// floor, so a full SHA and a 7-character short SHA compare as the same commit.
	minCommit = 7
)

// Release is a published release: its tag, the commit the tag points at when it could be
// resolved, and the asset name to download URL.
type Release struct {
	// Tag is the release tag, for example "v1.2.3".
	Tag string
	// Commit is the full commit SHA the tag resolves to, or empty when the API could not be
	// asked. Empty means "unknown": it is never treated as a differing commit, only as "no
	// opinion", so a failed lookup degrades to tag-only comparison.
	Commit string
	// Assets maps an asset file name to its download URL.
	Assets map[string]string
}

// Client talks to the release API. A nil HTTP uses http.DefaultClient.
type Client struct {
	// HTTP is the client used for every request. Nil means http.DefaultClient.
	HTTP *http.Client
	// API is the latest-release endpoint, normally DefaultAPI.
	API string
}

// get fetches url and returns the body. A body over limit bytes is an error rather
// than a silent truncation, because a truncated archive would fail its checksum anyway.
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
		// A nil client must still bound its request, or a stuck server hangs the caller forever.
		hc = &http.Client{Timeout: 2 * time.Minute}
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

	// The release payload carries no commit, so the tag is resolved separately. A failure here
	// is not a failure of the check: the tag comparison still stands on its own.
	if base, ok := strings.CutSuffix(c.API, latestSuffix); ok {
		if sha, err := c.tagCommit(ctx, base, rel.Tag); err == nil {
			rel.Commit = sha
		}
	}

	return rel, nil
}

// tagCommit resolves a tag to the commit it points at, through the commits endpoint. GitHub
// dereferences an annotated tag there itself, so one request is enough.
func (c Client) tagCommit(ctx context.Context, base, tag string) (string, error) {
	body, err := c.get(ctx, base+"/commits/"+url.PathEscape(tag), "application/vnd.github+json", 1<<20)
	if err != nil {
		return "", err
	}

	var raw struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("decode commit: %w", err)
	}

	if raw.SHA == "" {
		return "", errors.New("commit has no sha")
	}

	return raw.SHA, nil
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

// verify checks data against the sha256 listed for name in a checksums.txt body.
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
	// EqualFold because a checksums file may list the hex digest in upper case.
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return fmt.Errorf("checksum mismatch for %s", name)
	}

	return nil
}

// extract returns the executable from a release zip held in memory.
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

// ShouldUpdate reports whether the running build should be replaced by the release. It is true
// when the release tag is newer, and also when the tag is the same but the running build came
// from a different commit: a re-tagged release is still a different binary.
//
// The commit comparison is deliberately one-sided on missing data. It only fires when both
// commits are known and neither is dirty, because an unknown commit means "no opinion" and
// guessing would reinstall the same binary on every check. It is also unordered: a tag that
// moves backwards reads as different, which is what a re-tag is, so the caller's interval is
// what bounds any back-and-forth.
func ShouldUpdate(latestTag, latestCommit, curTag, curCommit string) (bool, error) {
	newer, err := Newer(latestTag, curTag)
	if err != nil {
		return false, err
	}

	if newer {
		return true, nil
	}

	// Same tag only. A newer running build, or an unparsable one, is not a downgrade candidate.
	l, err := parseVersion(latestTag)
	if err != nil {
		return false, err
	}

	c, err := parseVersion(curTag)
	if err != nil {
		return false, err
	}

	if l != c {
		return false, nil
	}

	// A dirty build has local changes the release cannot carry, so its commit never matches.
	if strings.Contains(curTag, "dirty") || !knownCommit(curCommit) {
		return false, nil
	}

	if !knownCommit(latestCommit) {
		return false, nil
	}

	return !sameCommit(latestCommit, curCommit), nil
}

// knownCommit reports whether a commit value identifies a commit. The linker default "none"
// and an unresolved empty value both mean the build cannot be told apart by commit.
func knownCommit(c string) bool {
	return c != "" && c != "none"
}

// sameCommit compares two commit values that may differ in length, because the release and the
// linker abbreviate a SHA independently. Equal under a common prefix of at least minCommit
// characters is the same commit. Two values shorter than that cannot be told apart, so they are
// reported as the same: an ambiguous pair must never trigger a reinstall.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)

	n := min(len(a), len(b))
	if n < minCommit {
		return true
	}

	return a[:n] == b[:n]
}

// Newer reports whether latest is a higher X.Y.Z than current. Any "-pre" or "+meta" suffix
// is ignored, so git-describe builds compare by their base tag.
// pre-release ordering is not handled; switch to x/mod/semver if it is needed.
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

// parseVersion parses [v]X.Y.Z and ignores any "-pre" or "+meta" suffix.
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
