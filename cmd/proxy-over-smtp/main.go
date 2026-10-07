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
