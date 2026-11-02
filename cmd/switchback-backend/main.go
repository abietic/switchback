package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abietic/switchback/internal/localproduct"
	"github.com/abietic/switchback/internal/platform/qualificationio"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runBackend(ctx, os.Args[1:], os.Stdout, backendOperations{func(lookup func(string) (string, bool)) (localproduct.Config, error) {
		return localproduct.LoadConfig(lookup)
	}, localproduct.AwaitRuntimeCapacityBootstrap, serveBackend, qualificationio.RunBackend}); err != nil {
		slog.Error("switchback backend stopped", "error", err)
		os.Exit(1)
	}
}

type backendOperations struct {
	load    func(func(string) (string, bool)) (localproduct.Config, error)
	await   func(context.Context, localproduct.Config) error
	start   func(context.Context, localproduct.Config) error
	qualify func(context.Context, io.Writer) error
}

func runBackend(ctx context.Context, args []string, out io.Writer, ops backendOperations) error {
	if len(args) == 1 && args[0] == "qualify-backend" {
		return ops.qualify(ctx, out)
	}
	if len(args) != 0 {
		return fmt.Errorf("backend argument is unsupported")
	}
	configuration, err := ops.load(os.LookupEnv)
	if err != nil {
		return err
	}
	if err = ops.await(ctx, configuration); err != nil {
		return err
	}
	return ops.start(ctx, configuration)
}

func serveBackend(ctx context.Context, configuration localproduct.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	app, err := localproduct.New(ctx, configuration)
	if err != nil {
		return err
	}
	defer app.Close()
	go func() {
		if err := app.Run(ctx); err != nil {
			slog.Error("local product background loop stopped", "error", err)
			cancel()
		}
	}()

	server := &http.Server{
		Addr:              configuration.Profile.WebAddress,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go shutdownOnCancel(ctx, server)

	slog.Info("switchback backend listening", "address", configuration.Profile.WebAddress, "profile", configuration.Profile.Name)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func shutdownOnCancel(ctx context.Context, server *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("switchback backend shutdown failed", "error", err)
	}
}
