package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func run(ctx context.Context, args []string, collect func(context.Context) error) error {
	if len(args) != 0 {
		return fmt.Errorf("qualification host takes no arguments")
	}
	return collect(ctx)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], capacitysnapshot.CollectHostQualification); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
