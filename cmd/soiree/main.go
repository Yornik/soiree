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

	srv, err := httpd.New(cfg, web.FS())
	if err != nil {
		log.Error("failed to build server", "err", err)
		os.Exit(1)
	}

	// Optional on purpose: with no DATABASE_URL the process serves the static
	// shell and nothing else, which is what a bare `docker run` does.
	var accounts *httpd.Auth
	if cfg.DatabaseURL != "" {
		pool, err := openDatabase(context.Background(), cfg, log)
		if err != nil {
			log.Error("database unavailable", "err", err)
			os.Exit(1)
		}
		defer pool.Close()

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
			Store:             store.New(pool),
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

// startupTimeout bounds connecting and migrating, so a database that accepts
// the connection and then stalls fails the pod rather than hanging it.
const startupTimeout = 60 * time.Second

// openDatabase connects, applies the migrations and creates the bootstrap
// admin if one was asked for and none exists.
func openDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := migrate.Run(ctx, pool, log); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

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
