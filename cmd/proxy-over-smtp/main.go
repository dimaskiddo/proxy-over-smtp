// Command proxy-over-smtp tunnels SOCKS4/5, HTTP and HTTPS proxy traffic through a fake SMTP
// session. This file only wires the signal context, build info and restart; the work is in
// internal/cli.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dimaskiddo/proxy-over-smtp/internal/cli"
	"github.com/dimaskiddo/proxy-over-smtp/internal/update"
)

// Build info, overwritten at link time through -ldflags -X by the Makefile, GoReleaser and the
// Dockerfile.
var (
	version = "dev"
	commit  = "none"
)

// main runs the CLI, re-execs the binary after an auto-update and exits 1 on error.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	restart, err := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit})
	stop()

	if restart {
		if err = update.Restart(); err != nil {
			fmt.Fprintln(os.Stderr, "restart:", err)
		}
	}

	if err != nil {
		os.Exit(1)
	}
}
