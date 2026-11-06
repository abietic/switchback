package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

type commands struct {
	intent, begin, prepare, collect, assemble func(context.Context) error
}

func run(ctx context.Context, args []string, c commands) error {
	if len(args) != 1 {
		return fmt.Errorf("qualification native requires exactly one fixed phase")
	}
	switch args[0] {
	case "intent":
		return c.intent(ctx)
	case "begin":
		return c.begin(ctx)
	case "prepare":
		return c.prepare(ctx)
	case "collect":
		return c.collect(ctx)
	case "assemble":
		return c.assemble(ctx)
	default:
		return fmt.Errorf("qualification native phase invalid")
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], commands{capacitysnapshot.PrepareNativeQualificationIntent, capacitysnapshot.BeginNativeQualification, capacitysnapshot.PrepareNativeQualification,
		capacitysnapshot.CollectNativeQualification, capacitysnapshot.AssembleQualification}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
