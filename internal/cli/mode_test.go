package cli

import (
	"io"
	"os"
	"slices"
	"testing"

	"github.com/spf13/pflag"
)

// modeFlags returns the root's persistent flag set, which is what resolveModeArg skips values
// through.
func modeFlags() *pflag.FlagSet {
	return newRoot(BuildInfo{}, io.Discard).PersistentFlags()
}

func TestResolveModeArg(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     string
		want    []string
		wantErr bool
	}{
		{"no args selects server", nil, "server", []string{"server"}, false},
		{"no args selects client", []string{}, "client", []string{"client"}, false},
		{"blank env is unset", nil, "   ", nil, false},
		{"value is trimmed and lowercased", nil, " Client ", []string{"client"}, false},
		{"subcommand wins", []string{"client"}, "server", []string{"client"}, false},
		{"subcommand after persistent flag wins", []string{"--log-level", "debug", "server"}, "client", []string{"--log-level", "debug", "server"}, false},
		{"persistent flag with equals", []string{"--log-level=debug"}, "server", []string{"server", "--log-level=debug"}, false},
		{"empty flag value is not positional", []string{"--log-file", ""}, "client", []string{"client", "--log-file", ""}, false},
		{"help wins", []string{"--help"}, "server", []string{"--help"}, false},
		{"short help wins", []string{"-h"}, "server", []string{"-h"}, false},
		{"version flag wins", []string{"--version"}, "server", []string{"--version"}, false},
		{"short version flag wins", []string{"-v"}, "server", []string{"-v"}, false},
		{"version command wins", []string{"version"}, "server", []string{"version"}, false},
		{"update command ignores mode", []string{"update"}, "server", []string{"update"}, false},
		{"help topic wins", []string{"help"}, "server", []string{"help"}, false},
		{"terminator exposes the subcommand", []string{"--", "server"}, "client", []string{"--", "server"}, false},
		{"bare terminator names no positional", []string{"--"}, "server", []string{"server", "--"}, false},
		{"unknown command is not overridden", []string{"nope"}, "server", []string{"nope"}, false},
		{"invalid mode", nil, "bogus", nil, true},
		{"invalid mode is ignored with a subcommand", []string{"client"}, "bogus", []string{"client"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(modeEnv, tt.env)

			got, err := resolveModeArg(modeFlags(), tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}

			if !slices.Equal(got, tt.want) {
				t.Fatalf("args = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveModeArgUnset covers the unset branch: t.Setenv cannot unset a variable, so the test
// sets one first to register its cleanup and then removes it.
func TestResolveModeArgUnset(t *testing.T) {
	t.Setenv(modeEnv, "server")
	os.Unsetenv(modeEnv)

	got, err := resolveModeArg(modeFlags(), []string{"--log-level", "debug"})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got, []string{"--log-level", "debug"}) {
		t.Fatalf("args = %q, want unchanged", got)
	}
}

// TestResolveModeArgDispatch checks the injected argument lands where cobra looks for a command.
func TestResolveModeArgDispatch(t *testing.T) {
	t.Setenv(modeEnv, "server")

	root := newRoot(BuildInfo{}, io.Discard)

	args, err := resolveModeArg(root.PersistentFlags(), nil)
	if err != nil {
		t.Fatal(err)
	}

	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatal(err)
	}

	if cmd.Name() != "server" {
		t.Fatalf("dispatched to %q, want server", cmd.Name())
	}
}
