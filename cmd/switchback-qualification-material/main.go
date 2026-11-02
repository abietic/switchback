package main

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"os"
	"os/signal"
	"syscall"
)

func run(ctx context.Context, args []string, call func(context.Context, string) error) error {
	if len(args) != 1 {
		return fmt.Errorf("material requires one fixed phase")
	}
	return call(ctx, args[0])
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:], capacitysnapshot.GenerateQualificationMaterial); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
