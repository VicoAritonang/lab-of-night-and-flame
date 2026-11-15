// Local ASYNC runner for development. Production uses cmd/lambda and SQS.
package main

import (
	"context"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/config"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/engine"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/queue"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load("async")
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMax)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	defer db.Pool.Close()
	if err = db.Ready(ctx); err != nil {
		slog.Error("apply sql/init.sql first")
		os.Exit(1)
	}
	q, err := queue.New(ctx, db, "", cfg.Region, cfg.Concurrency)
	if err != nil {
		slog.Error("cannot initialize worker")
		os.Exit(1)
	}
	worker := engine.New(db, q, cfg)
	slog.Info("local ASYNC worker ready", "concurrency", cfg.Concurrency)
	if err = q.RunLocal(ctx, worker.Handle); err != nil {
		slog.Error("local worker stopped unexpectedly")
		os.Exit(1)
	}
}
