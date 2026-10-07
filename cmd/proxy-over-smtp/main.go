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

// Build info, overwritten at link time through -ldflags -X by GoReleaser and the Dockerfile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	restart, err := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date})
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
