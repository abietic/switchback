package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abietic/switchback/internal/localproduct"
)

func main() {
	configuration, err := localproduct.LoadConfig(os.LookupEnv)
	if err != nil {
		slog.Error("load local product configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := localproduct.New(ctx, configuration)
	if err != nil {
		slog.Error("compose local product", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	go func() {
		if err := app.Run(ctx); err != nil {
			slog.Error("local product background loop stopped", "error", err)
			stop()
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
		slog.Error("switchback backend stopped", "error", err)
		os.Exit(1)
	}
}

func shutdownOnCancel(ctx context.Context, server *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("switchback backend shutdown failed", "error", err)
	}
}
