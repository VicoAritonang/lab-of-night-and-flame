// Agent Reliability Lab server. Build with: go build -o arl .
//
// "arl suite ..." runs a Workbench regression suite against a running lab and
// exits non-zero when its gate fails (CI); "arl healthcheck" probes the server.
//
// By default (no ARL_SQS_QUEUE_URL) one process serves the HTTP API, the agent
// WebSocket, the embedded UI and runs the job worker: the all-in-one mode used
// by docker compose next to the agent under development. With SQS configured
// it becomes the SYNC gateway of the split AWS deployment (see DEPLOYMENT.md).
package main

import (
	"context"
	_ "embed"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/cli"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/config"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/engine"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/server"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	migrations "github.com/VicoAritonang/lab-of-night-and-flame/sql"
	"github.com/VicoAritonang/lab-of-night-and-flame/web"
)

//go:embed connection.md
var connectionDoc string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) > 1 && os.Args[1] == "suite" {
		os.Exit(cli.Suite(os.Args[2:], os.Stdout, os.Stderr))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load("sync")
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if cfg.EmbeddedWorker {
		// The in-process worker reports provider keys from this environment.
		cfg.Mode = "all"
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMax)
	for attempt := 1; err != nil && cfg.AutoMigrate && attempt <= 30; attempt++ {
		// docker compose may start the Lab before PostgreSQL accepts connections.
		slog.Info("waiting for PostgreSQL", "attempt", attempt)
		select {
		case <-ctx.Done():
			os.Exit(1)
		case <-time.After(2 * time.Second):
		}
		db, err = store.Open(ctx, cfg.DatabaseURL, cfg.DBMax)
	}
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	defer db.Pool.Close()
	if cfg.AutoMigrate {
		if _, err = db.Pool.Exec(ctx, migrations.Init); err != nil {
			slog.Error("cannot apply sql/init.sql", "error", err.Error())
			os.Exit(1)
		}
	}
	if err = db.Ready(ctx); err != nil {
		slog.Error("schema missing; set ARL_AUTO_MIGRATE=true or run sql/init.sql using psql first")
		os.Exit(1)
	}
	app, err := server.New(ctx, cfg, db, connectionDoc)
	if err != nil {
		slog.Error("cannot initialize gateway")
		os.Exit(1)
	}
	defer app.Hub.Close()
	app.Hub.SetAlwaysOn(cfg.ListenOnStart)
	if cfg.ServeUI {
		app.UI = web.Dist()
	}
	if cfg.EmbeddedWorker {
		worker := engine.New(db, app.Q, cfg)
		go func() {
			if err := app.Q.RunLocal(ctx, worker.Handle); err != nil {
				slog.Error("embedded worker stopped unexpectedly")
				cancel()
			}
		}()
	}
	go engine.Periodic(ctx, db, app.Q)
	if cfg.QueueURL != "" {
		go app.Q.Dispatch(ctx)
	}
	_, _ = app.Q.Enqueue(ctx, "datasets.seed", domain.M{}, "seed:v2")
	_, _ = app.Q.Enqueue(ctx, "catalog.refresh", domain.M{}, "catalog:startup:"+time.Now().UTC().Format("2006-01-02"))
	srv := &http.Server{Addr: cfg.Address, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		slog.Info("Agent Reliability Lab ready", "address", cfg.Address, "embedded_worker", cfg.EmbeddedWorker, "always_listening", cfg.ListenOnStart, "ui", cfg.ServeUI, "sqs_configured", cfg.QueueURL != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP listener failed")
			cancel()
		}
	}()
	<-ctx.Done()
	app.Hub.Close()
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	_ = srv.Shutdown(shutdown)
}

// healthcheck lets the distroless image probe itself (no curl inside).
func healthcheck() int {
	addr := config.Env("ARL_ADDR", ":8000")
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 1
	}
	return 0
}
