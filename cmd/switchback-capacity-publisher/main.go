// Command switchback-capacity-publisher has no request or configuration channel.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitypublisher"
)

func run(ctx context.Context, args []string, publish func(context.Context) error) int {
	if ctx == nil || len(args) != 1 || publish == nil {
		return 2
	}
	if err := publish(ctx); err != nil {
		return 1
	}
	return 0
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args, capacitypublisher.Run)
	stop()
	if code != 0 {
		fmt.Fprintln(os.Stderr, "capacity_publisher_rejected")
	}
	os.Exit(code)
}
