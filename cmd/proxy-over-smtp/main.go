package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dimaskiddo/proxy-over-smtp/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date})
	stop()

	if err != nil {
		os.Exit(1)
	}
}
