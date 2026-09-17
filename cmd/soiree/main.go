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
	"github.com/Yornik/soiree/internal/mail"
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
	// Also the default, so a library that has no logger handed to it — the
	// API's 500 path — still lands in the JSON stream the cluster collects
	// rather than in plain text on stderr.
	slog.SetDefault(log)

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
	//
	// One pool, shared by the API and by accounts. They were built in
	// parallel and each opened its own; two pools against one database doubles
	// the connection count for nothing and gives the two halves of the same
	// process independent views of its health.
	var (
		opts     []httpd.Option
		accounts *httpd.Auth
		st       *store.Store
	)
	if cfg.DatabaseURL != "" {
		pool, err := openDatabase(ctx, cfg, log)
		if err != nil {
			log.Error("database unavailable", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		st = store.New(pool)
		opts = append(opts, httpd.WithStore(st))
	} else {
		log.Info("no DATABASE_URL set, serving the frontend only and leaving the API unmounted")
	}

	srv, err := httpd.New(cfg, web.FS(), opts...)
	if err != nil {
		log.Error("failed to build server", "err", err)
		os.Exit(1)
	}

	if st != nil {
		var mailer httpd.Mailer
		if cfg.SMTP.Enabled() {
			// Assigned only when configured: a typed nil in an interface is
			// not nil, and the accounts surface reads a nil Mailer as "hand
			// the link back to the admin instead".
			mailer = mail.New(mail.Config{
				Host:     cfg.SMTP.Host,
				Port:     cfg.SMTP.Port,
				Username: cfg.SMTP.Username,
				Password: cfg.SMTP.Password,
				From:     cfg.SMTP.From,
			})
		}

		accounts = httpd.NewAuth(httpd.AuthOptions{
			Store:             st,
			Mailer:            mailer,
			Logger:            log,
			BaseURL:           cfg.BaseURL,
			TrustProxyHeaders: cfg.TrustProxyHeaders,
		})
		srv = srv.WithAuth(accounts)
		log.Info("accounts enabled", "mail", cfg.SMTP.Enabled(), "baseURL", cfg.BaseURL)
	}

	hs := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: httpd.ReadHeaderTimeout,
		ReadTimeout:       httpd.ReadTimeout,
		WriteTimeout:      httpd.WriteTimeout,
		IdleTimeout:       httpd.IdleTimeout,
	}

	// Housekeeping: expired sessions and spent links. Tied to the signal
	// context, so it stops when the process is asked to.
	if accounts != nil {
		go accounts.Sweep(ctx)
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

// openDatabase connects, proves the connection works, brings the schema up to
// date, and creates the bootstrap admin if one was asked for and none exists.
//
// Migrations run here rather than as a separate job because the advisory lock
// inside the runner already makes concurrent replicas safe, and a schema that
// arrives with the code it belongs to cannot be forgotten.
//
// The connect and migrate phases are bounded separately: a DSN typo should
// fail as "cannot reach the database" within seconds, while the migration
// phase needs room because replicas queue on the advisory lock and the last
// one waits for every migration the first one runs.
func openDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
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

	if cfg.BootstrapAdmin != "" {
		// Creates an `invited` admin with no password and no link: the person
		// named picks their own password through the normal flow. It exists
		// only so that "every account is created by an admin" has somewhere to
		// start on an empty database.
		created, err := store.New(pool).EnsureBootstrapAdmin(ctx, cfg.BootstrapAdmin)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("bootstrap admin: %w", err)
		}
		if created {
			log.Info("bootstrap admin created", "email", cfg.BootstrapAdmin,
				"next", "request a set-password link from POST /api/v1/auth/password-reset")
		}
	}

	return pool, nil
}
