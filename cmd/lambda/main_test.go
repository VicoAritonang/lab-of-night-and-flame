package main

import (
	"context"
	"github.com/aws/aws-lambda-go/events"
	"go.vicoworks.com/agent-reliability-lab/internal/engine"
	"go.vicoworks.com/agent-reliability-lab/internal/queue"
	"go.vicoworks.com/agent-reliability-lab/internal/testutil"
	"testing"
)

func TestSQSPartialFailuresAndDuplicateDelivery(t *testing.T) {
	db := testutil.DB(t)
	cfg := testutil.Config()
	q, _ := queue.New(context.Background(), db, "", "", 4)
	runtimeApp = &app{q, engine.New(db, q, cfg), 4}
	initErr = nil
	t.Cleanup(func() { runtimeApp = nil; initErr = nil })
	id, err := q.Enqueue(context.Background(), "datasets.seed", map[string]any{}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	event := events.SQSEvent{Records: []events.SQSMessage{{MessageId: "good", Body: "{\"job_id\":\"" + id + "\"}"}, {MessageId: "bad", Body: "invalid JSON"}}}
	out, err := handler(context.Background(), event)
	if err != nil || len(out.BatchItemFailures) != 1 || out.BatchItemFailures[0].ItemIdentifier != "bad" {
		t.Fatal(out, err)
	}
	event.Records = event.Records[:1]
	out, err = handler(context.Background(), event)
	if err != nil || len(out.BatchItemFailures) != 0 {
		t.Fatal("duplicate redelivery failed", out, err)
	}
}
