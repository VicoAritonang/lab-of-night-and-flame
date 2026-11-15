// Build linux/arm64, CGO_ENABLED=0, -tags lambda.norpc, output bootstrap.
package main

import (
	"context"
	"errors"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/config"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/engine"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/queue"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

type app struct {
	Q      *queue.Queue
	Worker *engine.Engine
	N      int
}

var initMu sync.Mutex
var runtimeApp *app
var initErr error

func initialize(ctx context.Context) {
	// Keep a quarter of the invocation memory available for stacks, runtime and I/O.
	// Respect a stricter explicit GOMEMLIMIT while adapting to smaller deployments.
	if mib, err := strconv.ParseInt(os.Getenv("AWS_LAMBDA_FUNCTION_MEMORY_SIZE"), 10, 64); err == nil && mib > 0 {
		cap := mib * 1024 * 1024 * 3 / 4
		if current := debug.SetMemoryLimit(-1); current > cap {
			debug.SetMemoryLimit(cap)
		}
	}
	cfg, err := config.Load("async")
	if err != nil {
		initErr = err
		return
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMax)
	if err != nil {
		initErr = err
		return
	}
	if err = db.Ready(ctx); err != nil {
		db.Pool.Close()
		initErr = errors.New("PostgreSQL schema not initialized")
		return
	}
	q, err := queue.New(ctx, db, "", cfg.Region, cfg.Concurrency)
	if err != nil {
		db.Pool.Close()
		initErr = err
		return
	}
	runtimeApp = &app{q, engine.New(db, q, cfg), cfg.Concurrency}
}
func handler(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	initMu.Lock()
	if runtimeApp == nil {
		initErr = nil
		initialize(ctx)
	}
	ready := runtimeApp
	err := initErr
	initMu.Unlock()
	if err != nil || ready == nil {
		return events.SQSEventResponse{}, errors.New("worker configuration/database unavailable")
	}
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-8*time.Second))
		defer cancel()
	}
	response := events.SQSEventResponse{BatchItemFailures: []events.SQSBatchItemFailure{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtimeApp.N)
	failed := func(id string) {
		mu.Lock()
		response.BatchItemFailures = append(response.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: id})
		mu.Unlock()
	}
	for _, record := range event.Records {
		select {
		case slots <- struct{}{}:
			wg.Add(1)
			go func(record events.SQSMessage) {
				defer wg.Done()
				defer func() { <-slots }()
				id, err := queue.JobID(record.Body)
				if err == nil {
					err = runtimeApp.Q.Process(ctx, id, runtimeApp.Worker.Handle)
				}
				if err != nil {
					failed(record.MessageId)
					slog.Warn("async record will retry", "message_id", record.MessageId)
				}
			}(record)
		case <-ctx.Done():
			failed(record.MessageId)
		}
	}
	wg.Wait()
	return response, nil
}
func main() { lambda.Start(handler) }
