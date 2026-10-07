// Package cli defines the command tree: server, client, update and version. It maps flags and
// environment variables to configuration, builds the logger and drives graceful shutdown.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
	"github.com/dimaskiddo/proxy-over-smtp/internal/tunnel"
)

// defaultDrain is how long a stopping server or client waits for open connections.
const defaultDrain = 30 * time.Second

// app carries state shared by all commands: the logger, build info and the drain and restart
// settings. The logger exists only after PersistentPreRunE has run.
type app struct {
	out    io.Writer
	log    *slog.Logger
	closer io.Closer
	info   BuildInfo
	// restart is atomic because the auto-update goroutine sets it while Execute reads it.
	restart atomic.Bool
	drain   time.Duration

	logLevel, logFormat, logFile string
}

// Execute runs the command tree. Cobra prints the returned error. restart is true when an
// auto-update installed a new binary and the caller should re-exec it.
func Execute(ctx context.Context, info BuildInfo) (restart bool, err error) {
	root, a := newApp(info, os.Stdout)

	args, err := resolveModeArg(root.PersistentFlags(), os.Args[1:])
	if err != nil {
		// Cobra never runs, so it cannot print this one itself.
		root.PrintErrln(root.ErrPrefix(), err.Error())
		return false, err
	}

	root.SetArgs(args)
	err = root.ExecuteContext(ctx)

	// A failed run must not re-exec, even if an update was installed earlier.
	return err == nil && a.restart.Load(), err
}

// newApp builds the command tree and returns the app that owns its shared state.
func newApp(info BuildInfo, out io.Writer) (*cobra.Command, *app) {
	a := &app{out: out, info: info}

	root := &cobra.Command{
		Use:   "proxy-over-smtp",
		Short: "SOCKS4/5, HTTP and HTTPS proxy tunneled through a fake SMTP session",
		Long: "SOCKS4/5, HTTP and HTTPS proxy tunneled through a fake SMTP session.\n\n" +
			"With no subcommand, $PROXY_OVER_SMTP_MODE selects the mode: server or client. An explicit\n" +
			"subcommand always wins.",
		Version:       info.String(),
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyEnv(cmd.Flags()); err != nil {
				return err
			}

			l, c, err := newLogger(a.logLevel, a.logFormat, a.logFile, a.out)
			if err != nil {
				return err
			}

			a.log, a.closer = l, c
			return nil
		},
		PersistentPostRunE: func(*cobra.Command, []string) error {
			if a.closer == nil {
				return nil
			}

			return a.closer.Close()
		},
	}

	root.SetOut(out)
	root.SetVersionTemplate("{{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringVar(&a.logLevel, "log-level", "info", "Log level: debug, info, warn or error")
	pf.StringVar(&a.logFormat, "log-format", "text", "Log format: text or json")
	pf.StringVar(&a.logFile, "log-file", "", "Also write logs to this file (stdout only when empty)")

	root.AddCommand(newServer(a), newClient(a), newVersion(info), newUpdate(info))
	annotateEnv(root)

	return root, a
}

// newVersion returns the version command, which prints the one-line build info.
func newVersion(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
		},
	}
}

// newServer returns the server command. It validates flags before binding any port.
func newServer(a *app) *cobra.Command {
	var (
		cfg config.Config
		upd updateOpts
	)

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the tunnel server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}

			if err := upd.validate(); err != nil {
				return err
			}

			if err := a.validateDrain(); err != nil {
				return err
			}

			t, err := tunnel.New(cfg, a.log)
			if err != nil {
				return err
			}

			return a.run(cmd.Context(), t, upd, t.RunServer)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", "0.0.0.0:465", "Server listen address")
	f.StringVar(&cfg.Secret, "secret", "", "Shared secret: handshake authentication and stream key master (prefer the env var)")
	f.StringVar(&cfg.Cipher, "cipher", config.CipherAES, "Stream cipher: xor or aes (both ends must match)")
	f.IntVar(&cfg.MaxStreams, "max-streams", config.DefaultMaxStreams, "Max concurrent streams per session (server-enforced; on the client, the load at which the pool grows)")
	f.BoolVar(&cfg.AllowPrivate, "allow-private", false, "Allow loopback, private and link-local targets")
	f.DurationVar(&a.drain, "drain-timeout", defaultDrain, "How long to wait for open connections on shutdown")
	upd.bind(cmd)

	return cmd
}

// newClient returns the client command. It validates flags and loads the optional TLS key
// pair before binding any port.
func newClient(a *app) *cobra.Command {
	var (
		cfg config.Config
		upd updateOpts
	)

	cmd := &cobra.Command{
		Use:   "client",
		Short: "Run the local proxy client (SOCKS4/5, HTTP, HTTPS)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}

			if cfg.Remote == "" {
				return fmt.Errorf("remote address must not be empty")
			}

			if err := upd.validate(); err != nil {
				return err
			}

			if err := a.validateDrain(); err != nil {
				return err
			}

			t, err := tunnel.New(cfg, a.log)
			if err != nil {
				return err
			}

			return a.run(cmd.Context(), t, upd, t.RunClient)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", "0.0.0.0:1080", "Client proxy listen address (SOCKS4/5, HTTP, TLS)")
	f.StringVar(&cfg.Remote, "remote", "127.0.0.1:465", "Server address the client dials")
	f.StringVar(&cfg.Secret, "secret", "", "Shared secret: handshake authentication and stream key master (prefer the env var)")
	f.StringVar(&cfg.Cipher, "cipher", config.CipherAES, "Stream cipher: xor or aes (both ends must match)")
	f.IntVar(&cfg.MaxStreams, "max-streams", config.DefaultMaxStreams, "Max concurrent streams per session (server-enforced; on the client, the load at which the pool grows)")
	f.IntVar(&cfg.PoolMin, "pool-min", config.DefaultPoolMin, "Min tunnel sessions the client keeps open (client-only)")
	f.IntVar(&cfg.PoolMax, "pool-max", config.DefaultPoolMax, "Max tunnel sessions the client opens under load (client-only)")
	f.StringVar(&cfg.TLSCert, "tls-cert", "", "PEM certificate to also accept TLS proxy connections (needs --tls-key)")
	f.StringVar(&cfg.TLSKey, "tls-key", "", "PEM private key for --tls-cert")
	f.DurationVar(&a.drain, "drain-timeout", defaultDrain, "How long to wait for open connections on shutdown")
	upd.bind(cmd)

	return cmd
}

// validateDrain rejects a negative --drain-timeout. Zero means close connections at once.
func (a *app) validateDrain() error {
	if a.drain < 0 {
		return fmt.Errorf("drain-timeout must not be negative, got %s", a.drain)
	}

	return nil
}

// run starts fn and blocks until ctx is cancelled by a signal or an auto-update, which stops
// new connections. It then gives open connections a.drain to finish before closing them.
// A second SIGINT or SIGTERM during the drain closes them immediately.
func (a *app) run(ctx context.Context, t *tunnel.Tunnel, upd updateOpts, fn func(context.Context) error) error {
	if err := a.runWithUpdate(ctx, upd, fn); err != nil {
		return err
	}

	a.log.Info("draining", "active", t.Active(), "timeout", a.drain.String())

	// ctx is already cancelled here, so the drain window must start from a fresh context.
	dctx, cancel := context.WithTimeout(context.Background(), a.drain)
	defer cancel()

	dctx, stop := signal.NotifyContext(dctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := t.Shutdown(dctx); err != nil {
		a.log.Warn("drain interrupted, connections closed", "active", t.Active(), "reason", err.Error())

		// The stop was requested, so a forced drain is not a failed run.
		return nil
	}

	a.log.Info("shutdown complete")

	return nil
}
