package main

import (
	"context"
	"fmt"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"os"
	"os/signal"
	"syscall"
)

type commands struct{ preflight, bootstrap, install func(context.Context) error }

func run(ctx context.Context, args []string, c commands) error {
	if len(args) != 1 {
		return fmt.Errorf("qualification install requires one fixed phase")
	}
	switch args[0] {
	case "preflight":
		return c.preflight(ctx)
	case "bootstrap-publication":
		return c.bootstrap(ctx)
	case "install":
		return c.install(ctx)
	}
	return fmt.Errorf("qualification installation phase invalid")
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:], commands{capacitysnapshot.RecordQualificationPublicationEmpty, capacitysnapshot.BootstrapQualificationPublication, capacitysnapshot.InstallQualification}); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
