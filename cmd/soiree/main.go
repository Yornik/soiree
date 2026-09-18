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

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/httpd"
	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/reminders"
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

	// The accounts surface is built before the server, because the server
	// renders the client configuration into the page at construction and one of
	// the things that configuration says is whether passkeys are on offer. The
	// browser decides from it whether to show a passkey button, so it has to
	// agree with whether the routes are actually mounted — and whether they are
	// is only settled here.
	if st != nil {
		var accountMail httpd.Mailer
		if cfg.SMTP.Enabled() {
			// Assigned only when configured: a typed nil in an interface is
			// not nil, and the accounts surface reads a nil Mailer as "hand
			// the link back to the admin instead".
			accountMail = accountMailer{send: mailer.New(smtpConfig(cfg.SMTP))}
		}

		accounts = httpd.NewAuth(httpd.AuthOptions{
			Store:             st,
			Mailer:            accountMail,
			Logger:            log,
			BaseURL:           cfg.BaseURL,
			TrustProxyHeaders: cfg.TrustProxyHeaders,
			Locale:            cfg.Locale,
		})
		if err := accounts.WithPasskeys(cfg); err != nil {
			// Logged and carried on with, never fatal. Passkeys are additive:
			// without them every account is still reachable by its password and
			// by an admin's re-invite, so refusing to start would turn a
			// convenience this deployment cannot offer into an outage.
			log.Error("passkeys unavailable, leaving them off", "err", err)
			cfg.PasskeysEnabled = false
		}
		log.Info("accounts enabled", "mail", cfg.SMTP.Enabled(),
			"passkeys", cfg.PasskeysEnabled, "baseURL", cfg.BaseURL)
	} else {
		// No database, so no accounts and no passkeys, whatever the environment
		// asked for. This is the condition package config cannot see.
		cfg.PasskeysEnabled = false
	}

	srv, err := httpd.New(cfg, web.FS(), opts...)
	if err != nil {
		log.Error("failed to build server", "err", err)
		os.Exit(1)
	}
	if accounts != nil {
		srv = srv.WithAuth(accounts)
	}

	hs := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: httpd.ReadHeaderTimeout,
		ReadTimeout:       httpd.ReadTimeout,
		WriteTimeout:      httpd.WriteTimeout,
		IdleTimeout:       httpd.IdleTimeout,
	}
	// Shutdown closes the listeners and then waits for connections to go idle.
	// A live-sync stream blocked on its request context never does, so without
	// this one connected browser turns every SIGTERM into a ten-second hang
	// followed by a non-zero exit.
	hs.RegisterOnShutdown(srv.StopLiveSync)

	// The metrics exposition, on a port of its own. The public ingress route
	// carries no path constraint, so /metrics on the main listener would be
	// world-readable and soiree_build_info would name the running commit to
	// anyone who asked. The probes stay on the main listener, because that is
	// the port kubelet reaches.
	ms := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           srv.MetricsHandler(),
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

	// The deadline digest. Off unless SOIREE_REMINDER_ENABLED is true, and a
	// no-op when SMTP is unconfigured, so this costs nothing in a deployment
	// that does not want it. It holds a Postgres advisory lock while sending,
	// which is what stops three replicas mailing the same digest three times.
	if st != nil {
		stopReminders, err := reminders.Start(ctx, st, log)
		if err != nil {
			// Bad reminder configuration is a startup error rather than a
			// warning: a digest that silently never sends is the same failure
			// as having no reminders at all, which is the thing this feature
			// exists to prevent.
			log.Error("invalid reminder configuration", "err", err)
			os.Exit(1)
		}
		defer stopReminders()
	}

	go func() {
		log.Info("soiree listening",
			"addr", cfg.ListenAddr,
			"metricsAddr", cfg.MetricsAddr,
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

	// A metrics port that cannot bind takes the process down with it, exactly
	// as the main one does. The alternative is a pod that looks healthy while
	// every scrape fails, which is the failure nobody notices until they need
	// the graph.
	go func() {
		if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Both, then decide. Letting the first failure skip the second would leave
	// the metrics listener holding its connections open for the whole of the
	// termination grace period.
	if err := errors.Join(hs.Shutdown(shutdownCtx), ms.Shutdown(shutdownCtx)); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}

// smtpConfig maps the validated environment surface onto the transport.
//
// Two structs rather than one because they answer to different things:
// config.SMTPConfig is what the operator set and is checked against what a
// deployment needs (a sender with no relay is refused, a relay with no base URL
// is refused), while mailer.Config is what the SMTP conversation needs. This is
// the single place that knows the mapping.
func smtpConfig(c config.SMTPConfig) mailer.Config {
	return mailer.Config{
		Host:     c.Host,
		Port:     c.Port,
		Username: c.Username,
		Password: c.Password,
		From:     c.From,
	}
}

// accountMailer adapts internal/mailer to the interface internal/httpd asks
// for.
//
// The accounts surface wants one recipient, a subject and a plain-text body,
// and deliberately knows nothing else about mail — it is the package that
// decides what to say, not how to say it. internal/mailer wants a Message. The
// translation is this, and it is the whole of what used to be a second SMTP
// client.
//
// The error comes back unwrapped, so a caller that cares can still ask
// mailer.Ambiguous whether the message might have gone out. The accounts
// surface does not — it has nothing to retry and no ledger to release — but
// flattening the error here would take that away from whatever does next.
type accountMailer struct{ send mailer.Sender }

func (m accountMailer) Send(ctx context.Context, to, subject, body string) error {
	// Text only: a set-password link is a credential, and nothing about it
	// wants rendering. No HTML part means nothing in the mail can fetch
	// anything from anywhere, which is the same rule the digest follows.
	return m.send.Send(ctx, mailer.Message{
		To:      []string{to},
		Subject: subject,
		Text:    body,
	})
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
		//
		// Hashing here rather than in the store keeps the Argon2id policy in
		// one place: an initial password gets exactly the parameters every
		// other password gets, and is upgraded on login by the same code when
		// those parameters are raised.
		var hash string
		if cfg.BootstrapPassword != "" {
			h, err := auth.Hash(cfg.BootstrapPassword)
			if err != nil {
				pool.Close()
				return nil, fmt.Errorf("hash the bootstrap password: %w", err)
			}
			hash = h
		}

		created, err := store.New(pool).EnsureBootstrapAdmin(ctx, cfg.BootstrapAdmin, hash)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("bootstrap admin: %w", err)
		}
		if created {
			// Never the password, and never the link. Which path was taken is
			// operationally useful; the credential itself is not.
			next := "request a set-password link from POST /api/v1/auth/password-reset"
			if hash != "" {
				next = "log in with SOIREE_BOOTSTRAP_PASSWORD, then change it"
			}
			log.Info("bootstrap admin created", "email", cfg.BootstrapAdmin, "next", next)
		}
	}

	return pool, nil
}
