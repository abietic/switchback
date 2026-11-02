package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/abietic/switchback/internal/platform/qualificationio"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("qualification ioctl arguments are unsupported")
	}
	return qualificationio.RunCompat(ctx, out)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "qualification ioctl unavailable")
		os.Exit(1)
	}
}
