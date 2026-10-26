// Command eztest is the single composition root of the EzTest backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
)

const (
	// startupTimeout bounds database connection, migration and admin seeding
	// work at boot.
	startupTimeout = 30 * time.Second
	// bcryptCost is the documented password hashing cost.
	bcryptCost = 12
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eztest:", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := serve(cfg, logger); err != nil {
		logger.Error("eztest stopped with error", "error", err)
		os.Exit(1)
	}
}

// serve opens the database, applies pending migrations, then starts the HTTP
// server and shuts it down gracefully on SIGINT or SIGTERM.
func serve(cfg config.Config, logger *slog.Logger) error {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelStartup()

	database, err := db.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := db.MigrateEmbedded(startupCtx, database.Pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	userService := identityapp.NewUserService(
		identitypostgres.NewUserRepository(database.Pool),
		password.NewHasher(bcryptCost),
	)
	if err := userService.EnsureAdmin(startupCtx, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return fmt.Errorf("seed admin (set EZTEST_ADMIN_USERNAME and EZTEST_ADMIN_PASSWORD when no admin exists): %w", err)
	}
	cancelStartup()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.New(cfg, logger, server.Dependencies{Pinger: database}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop()
		logger.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
