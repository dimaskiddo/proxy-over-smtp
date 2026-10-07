package cli

import "runtime/debug"

// BuildInfo is injected by the linker from cmd/proxy-over-smtp.
type BuildInfo struct {
	// Version is the release tag, or "dev" for an unversioned build.
	Version string
	// Commit is the short git revision, or "none".
	Commit string
}

// resolved falls back to the module build info when the linker injected no version.
func (b BuildInfo) resolved() (version, commit string) {
	v, c := b.Version, b.Commit

	// A plain "go install" build has no ldflags but carries its version in the module build info.
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

// String formats the build info as a single human-readable line, "name version" or
// "name version~commit". The label itself comes from buildLabel, so the line and an update
// message can never render the same build two different ways.
func (b BuildInfo) String() string {
	v, c := b.resolved()

	return "Proxy-Over-SMTP " + buildLabel(v, c)
}
