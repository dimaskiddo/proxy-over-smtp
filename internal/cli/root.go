package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/dimaskiddo/proxy-over-smtp/internal/config"
	"github.com/dimaskiddo/proxy-over-smtp/internal/tunnel"
)

const drainTimeout = 5 * time.Second

type app struct {
	out     io.Writer
	log     *slog.Logger
	closer  io.Closer
	info    BuildInfo
	restart atomic.Bool

	logLevel, logFormat, logFile string
}

// Execute runs the command tree. Cobra prints the returned error. restart is true when an
// auto-update installed a new binary and the caller should re-exec it.
func Execute(ctx context.Context, info BuildInfo) (restart bool, err error) {
	root, a := newApp(info, os.Stdout)
	err = root.ExecuteContext(ctx)

	return err == nil && a.restart.Load(), err
}

func newRoot(info BuildInfo, out io.Writer) *cobra.Command {
	root, _ := newApp(info, out)
	return root
}

func newApp(info BuildInfo, out io.Writer) (*cobra.Command, *app) {
	a := &app{out: out, info: info}

	root := &cobra.Command{
		Use:           "proxy-over-smtp",
		Short:         "SOCKS5 proxy tunneled through a fake SMTP session",
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

func newVersion(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\nBy Dimas Restu H <drh.dimasrestu@gmail.com>\n", info.String())
		},
	}
}

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

			t := tunnel.New(cfg, a.log)
			return a.run(cmd.Context(), t, upd, t.RunServer)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", "0.0.0.0:465", "Server listen address")
	f.StringVar(&cfg.Secret, "secret", "", "Shared secret: EHLO token and XOR key (prefer the env var)")
	f.BoolVar(&cfg.AllowPrivate, "allow-private", false, "Allow loopback, private and link-local targets")
	upd.bind(cmd)

	return cmd
}

func newClient(a *app) *cobra.Command {
	var (
		cfg config.Config
		upd updateOpts
	)

	cmd := &cobra.Command{
		Use:   "client",
		Short: "Run the local SOCKS5 client",
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

			t := tunnel.New(cfg, a.log)
			return a.run(cmd.Context(), t, upd, t.RunClient)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", "0.0.0.0:1080", "Client SOCKS5 listen address")
	f.StringVar(&cfg.Remote, "remote", "127.0.0.1:465", "Server address the client dials")
	f.StringVar(&cfg.Secret, "secret", "", "Shared secret: EHLO token and XOR key (prefer the env var)")
	upd.bind(cmd)

	return cmd
}

// run blocks in fn until ctx is cancelled, then waits for active connections up to drainTimeout.
func (a *app) run(ctx context.Context, t *tunnel.Tunnel, upd updateOpts, fn func(context.Context) error) error {
	if err := a.runWithUpdate(ctx, upd, fn); err != nil {
		return err
	}

	a.log.Info("shutting down")

	done := make(chan struct{})
	go func() {
		t.Wait()
		close(done)
	}()

	select {
	case <-done:
		a.log.Info("shutdown complete")
	case <-time.After(drainTimeout):
		a.log.Warn("shutdown timed out, forcing exit")
	}

	return nil
}
