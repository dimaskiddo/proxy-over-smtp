package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// BuildInfo is injected by the linker from cmd/proxy-over-smtp.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// resolved falls back to the module build info when the linker injected no version.
func (b BuildInfo) resolved() (version, commit string) {
	v, c := b.Version, b.Commit

	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				v = bi.Main.Version
			}

			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && c == "none" && len(s.Value) >= 7 {
					c = s.Value[:7]
				}
			}
		}
	}

	return v, c
}

func (b BuildInfo) String() string {
	v, c := b.resolved()

	return fmt.Sprintf("Proxy-Over-SMTP %s (commit %s, built %s, %s %s/%s)",
		v, c, b.Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
