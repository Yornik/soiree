// Command soiree serves the event planner.
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/httpd"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

const (
	// connectTimeout bounds the first connection. pgxpool.New does not
	// connect, so without an explicit ping a typo in the DSN would surface
	// somewhere inside the migration runner instead of as "cannot reach the
	// database".
	connectTimeout = 10 * time.Second

	// migrateTimeout is generous because replicas queue on the advisory lock:
	// the last of them waits for every migration the first one runs.
	migrateTimeout = 5 * time.Minute
)

// Overridden at link time with -ldflags. Surfaced in the startup log and in
// the soiree_build_info metric, so a running pod can be tied to a commit.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	httpd.Version = version
	httpd.Commit = commit

	cfg, err := config.Load()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// No DSN is a supported configuration, not a missing one: the binary then
	// serves the frontend alone, which is what a bare `docker run` with no
	// database does.
	var opts []httpd.Option
	if cfg.DatabaseURL != "" {
		pool, err := openDatabase(ctx, log, cfg.DatabaseURL)
		if err != nil {
			log.Error("database unavailable", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		opts = append(opts, httpd.WithStore(store.New(pool)))
	} else {
		log.Info("no DATABASE_URL set, serving the frontend only and leaving the API unmounted")
	}

	srv, err := httpd.New(cfg, web.FS(), opts...)
	if err != nil {
		log.Error("failed to build server", "err", err)
		os.Exit(1)
	}

	hs := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: httpd.ReadHeaderTimeout,
		ReadTimeout:       httpd.ReadTimeout,
		WriteTimeout:      httpd.WriteTimeout,
		IdleTimeout:       httpd.IdleTimeout,
	}

	go func() {
		log.Info("soiree listening",
			"addr", cfg.ListenAddr,
			"version", version,
			"commit", commit,
			"event", cfg.EventName,
			"currency", cfg.Currency,
			"demoData", cfg.DemoData,
			"api", cfg.DatabaseURL != "",
		)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}

// openDatabase connects, proves the connection works, and brings the schema up
// to date before anything is served.
//
// Migrations run here rather than as a separate job because the advisory lock
// inside the runner already makes concurrent replicas safe, and a schema that
// arrives with the code it belongs to cannot be forgotten.
func openDatabase(ctx context.Context, log *slog.Logger, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}

	migrateCtx, cancelMigrate := context.WithTimeout(ctx, migrateTimeout)
	defer cancelMigrate()
	applied, err := migrate.Run(migrateCtx, pool, log)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	log.Info("database ready", "migrationsApplied", len(applied))
	return pool, nil
}
