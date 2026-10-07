package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/dimaskiddo/proxy-over-smtp/internal/update"
)

const (
	// minUpdateInterval keeps a mistyped interval from exhausting the GitHub API rate limit.
	minUpdateInterval = time.Hour
	// updateTimeout bounds one release check plus one archive download.
	updateTimeout = 2 * time.Minute
)

// errDevBuild is returned when update runs on a build without a release version.
var errDevBuild = errors.New("dev build has no comparable version: use --force")

// updateOpts holds the auto-update flags shared by server and client.
type updateOpts struct {
	enabled  bool
	interval time.Duration
	// api is the release endpoint. It is hidden and exists so tests can point at a local server.
	api string
}

// bind registers the auto-update flags on cmd.
func (o *updateOpts) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&o.enabled, "auto-update", false, "Check GitHub releases periodically, install a newer one and restart")
	f.DurationVar(&o.interval, "update-interval", 24*time.Hour, "Auto-update check interval (minimum 1h)")
	f.StringVar(&o.api, "update-api", update.DefaultAPI, "Release API URL")
	_ = f.MarkHidden("update-api")
}

// validate rejects an interval below minUpdateInterval so a typo cannot hammer the GitHub API.
func (o *updateOpts) validate() error {
	if o.enabled && o.interval < minUpdateInterval {
		return fmt.Errorf("update interval must be at least %s", minUpdateInterval)
	}

	return nil
}

// updateClient returns an update client for api with the update timeout applied.
func updateClient(api string) update.Client {
	return update.Client{HTTP: &http.Client{Timeout: updateTimeout}, API: api}
}

// buildLabel is the one place a version and commit become a label, so an update message and the
// running binary's own line (BuildInfo.String) can never disagree. An unknown commit is left off
// rather than shown as "none".
func buildLabel(version, commit string) string {
	if commit == "" || commit == "none" {
		return version
	}

	return version + "~" + commit
}

// newUpdate returns the update command. With --check it only reports; otherwise it installs
// the latest release over the running executable.
func newUpdate(info BuildInfo) *cobra.Command {
	var (
		check, force bool
		api          string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update this binary to the latest GitHub release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cur, curCommit := info.resolved()
			if cur == "dev" && !force {
				return errDevBuild
			}

			c := updateClient(api)

			rel, err := c.Latest(cmd.Context())
			if err != nil {
				return fmt.Errorf("check latest release: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "current %s, latest %s\n", buildLabel(cur, curCommit), buildLabel(rel.Tag, rel.Commit))

			if check {
				return nil
			}

			if !force {
				// A newer tag or the same tag from a different commit both count as "replace it".
				replace, err := update.ShouldUpdate(rel.Tag, rel.Commit, cur, curCommit)
				if err != nil {
					return fmt.Errorf("compare versions: %w", err)
				}

				if !replace {
					fmt.Fprintln(out, "already up to date")
					return nil
				}
			}

			exe, err := update.ExecutablePath()
			if err != nil {
				return err
			}

			if err := c.Apply(cmd.Context(), rel, exe); err != nil {
				return fmt.Errorf("apply update: %w", err)
			}

			fmt.Fprintf(out, "updated to %s; restart running instances to apply\n", buildLabel(rel.Tag, rel.Commit))

			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&check, "check", false, "Only report the current and latest version")
	f.BoolVar(&force, "force", false, "Install even when up to date or on a dev build")
	f.StringVar(&api, "update-api", update.DefaultAPI, "Release API URL")
	_ = f.MarkHidden("update-api")

	return cmd
}

// autoUpdate checks once at start and then every interval until ctx ends. After installing
// a release it asks for a restart and cancels the run so the normal drain happens first.
func (a *app) autoUpdate(ctx context.Context, cancel context.CancelFunc, o updateOpts) {
	cur, curCommit := a.info.resolved()
	if cur == "dev" {
		a.log.Warn("auto-update disabled for dev build")
		return
	}

	c := updateClient(o.api)

	tick := time.NewTicker(o.interval)
	defer tick.Stop()

	for {
		if a.checkAndInstall(ctx, c, cur, curCommit) {
			a.restart.Store(true)
			cancel()

			return
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// checkAndInstall reports whether a release was installed.
func (a *app) checkAndInstall(ctx context.Context, c update.Client, cur, curCommit string) bool {
	rel, err := c.Latest(ctx)
	if err != nil {
		a.log.Warn("update check failed", "err", err)
		return false
	}

	replace, err := update.ShouldUpdate(rel.Tag, rel.Commit, cur, curCommit)
	if err != nil {
		a.log.Warn("update check failed", "err", err)
		return false
	}

	if !replace {
		a.log.Debug("already up to date", "version", buildLabel(cur, curCommit), "latest", buildLabel(rel.Tag, rel.Commit))
		return false
	}

	exe, err := update.ExecutablePath()
	if err == nil {
		err = c.Apply(ctx, rel, exe)
	}

	if err != nil {
		a.log.Warn("update failed", "err", err)
		return false
	}

	a.log.Info("update installed, restarting", "from", buildLabel(cur, curCommit), "to", buildLabel(rel.Tag, rel.Commit))

	return true
}

// runWithUpdate runs fn with the auto-update loop alongside and waits for the loop to exit.
func (a *app) runWithUpdate(ctx context.Context, o updateOpts, fn func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if !o.enabled {
		return fn(ctx)
	}

	var wg sync.WaitGroup
	wg.Go(func() { a.autoUpdate(ctx, cancel, o) })

	err := fn(ctx)

	// fn can return on its own, for example on a listen error. Cancel so the update loop
	// stops and wg.Wait does not block forever.
	cancel()
	wg.Wait()

	return err
}
