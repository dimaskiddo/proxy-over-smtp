package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// envPrefix namespaces every environment variable the CLI reads.
const envPrefix = "PROXY_OVER_SMTP_"

// envName returns the environment variable that backs flag, for example "log-level" maps to
// PROXY_OVER_SMTP_LOG_LEVEL.
func envName(flag string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// applyEnv fills flags the user did not pass from the environment: flag > env > default.
func applyEnv(fs *pflag.FlagSet) error {
	var err error

	fs.VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Changed {
			return
		}

		v, ok := os.LookupEnv(envName(f.Name))
		if !ok {
			return
		}

		if e := fs.Set(f.Name, v); e != nil {
			err = fmt.Errorf("invalid %s: %w", envName(f.Name), e)
		}
	})

	return err
}

// annotateEnv appends the env var name to every flag's help text.
func annotateEnv(cmd *cobra.Command) {
	add := func(f *pflag.Flag) {
		if f.Name != "help" && f.Name != "version" {
			f.Usage += " [$" + envName(f.Name) + "]"
		}
	}

	cmd.LocalFlags().VisitAll(add)

	for _, c := range cmd.Commands() {
		annotateEnv(c)
	}
}
