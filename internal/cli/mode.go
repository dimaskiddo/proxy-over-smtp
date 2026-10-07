package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

// modeEnv names the environment variable that selects the mode to run when the command line names
// no subcommand. It reuses envPrefix so the name stays in step with every flag's.
const modeEnv = envPrefix + "MODE"

// resolveModeArg inserts the mode named by PROXY_OVER_SMTP_MODE as the subcommand when args name
// none, and returns args unchanged otherwise. Only server and client are accepted: update and
// version stay argv-only, so pointing the environment at a mode never self-replaces a binary.
//
// An explicit position always wins. The environment never overrides a command the user typed, and
// a value that is set but invalid is an error rather than a silent fallback to the help text.
//
// The environment is read here rather than in applyEnv because applyEnv runs from
// PersistentPreRunE, after cobra has already dispatched, which is too late to pick a command. fs is
// the root's persistent flag set, used only to skip the values of flags that may precede the
// subcommand.
func resolveModeArg(fs *pflag.FlagSet, args []string) ([]string, error) {
	if firstPositional(fs, args) != "" || helpOrVersion(args) {
		return args, nil
	}

	v, ok := os.LookupEnv(modeEnv)
	if !ok {
		return args, nil
	}

	mode := strings.ToLower(strings.TrimSpace(v))
	if mode == "" {
		return args, nil
	}

	if mode != "server" && mode != "client" {
		return nil, fmt.Errorf("invalid %s %q: want server or client", modeEnv, v)
	}

	return append([]string{mode}, args...), nil
}

// firstPositional returns the token cobra reads as the subcommand or first positional argument, or
// "" when the command line has none. A flag's value is skipped, because persistent flags may
// precede the subcommand ("--log-level debug server"), and "--" ends flag parsing.
func firstPositional(fs *pflag.FlagSet, args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}

			return ""
		}

		if !strings.HasPrefix(a, "-") || a == "-" {
			return a
		}

		// A flag written as --name=value carries its own value, so only a bare name can consume
		// the token after it.
		if f := fs.Lookup(strings.TrimLeft(a, "-")); f != nil && f.NoOptDefVal == "" {
			i++
		}
	}

	return ""
}

// helpOrVersion reports whether args ask for help or the version banner. Those must reach the root
// command unchanged, or a mode in the environment would turn root help into server help.
func helpOrVersion(args []string) bool {
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "--help", "-v", "--version":
			return true
		}
	}

	return false
}
