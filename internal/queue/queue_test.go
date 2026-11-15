package queue

import (
	"context"
	"errors"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/testutil"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDurableDedupAndConcurrentClaims(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 4)
	ctx := context.Background()
	id, err := q.Enqueue(ctx, "test", M{"x": 1}, "same")
	if err != nil {
		t.Fatal(err)
	}
	same, err := q.Enqueue(ctx, "test", M{"x": 1}, "same")
	if err != nil || id != same {
		t.Fatal("dedup failed")
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := q.Process(ctx, id, func(context.Context, *Job) (M, error) { calls.Add(1); return M{"ok": true}, nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate execution", calls.Load())
	}
	result, err := q.Result(ctx, id)
	if err != nil || result["ok"] != true {
		t.Fatal(result, err)
	}
}
func TestJobAndDocumentCommitTogether(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 2)
	ctx := context.Background()
	id, err := q.Enqueue(ctx, "test", M{}, "atomic", Doc{"campaigns", "c", M{"status": "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Get(ctx, "campaigns", "c"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.Pool.QueryRow(ctx, "SELECT count(*) FROM arl.jobs WHERE id=$1", id).Scan(&n)
	if n != 1 {
		t.Fatal("outbox missing")
	}
}
func TestDeferredWorkDoesNotConsumeRetryBudget(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 2)
	ctx := context.Background()
	id, _ := q.Enqueue(ctx, "test", M{}, "defer")
	if err := q.Process(ctx, id, func(context.Context, *Job) (M, error) { return nil, ErrDefer }); err != nil {
		t.Fatal(err)
	}
	var state string
	var n int
	if err := db.Pool.QueryRow(ctx, "SELECT state,attempts FROM arl.jobs WHERE id=$1", id).Scan(&state, &n); err != nil || state != "queued" || n != 0 {
		t.Fatal(state, n, err)
	}
}
func TestRetryLimitAndLeaseRecovery(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 2)
	ctx := context.Background()
	id, _ := q.Enqueue(ctx, "test", M{}, "failure")
	for i := 0; i < 4; i++ {
		_ = q.Process(ctx, id, func(context.Context, *Job) (M, error) { return nil, errors.New("simulated failure") })
	}
	if _, err := q.Result(ctx, id); err == nil {
		t.Fatal("failed job looks successful")
	}
	other, _ := q.Enqueue(ctx, "test", M{}, "expired")
	j, _ := q.Claim(ctx, other)
	if j == nil {
		t.Fatal("cannot claim")
	}
	_, _ = db.Pool.Exec(ctx, "UPDATE arl.jobs SET lease_until=now()-interval '1 second' WHERE id=$1", other)
	reclaimed, err := q.Claim(ctx, other)
	if err != nil || reclaimed == nil || reclaimed.Token == j.Token {
		t.Fatal("lease did not recover", err)
	}
}
func TestBatchQueueIdempotency(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 2)
	items := []Item{}
	for i := 0; i < 450; i++ {
		items = append(items, Item{"test", domain.Str(i), M{"i": i}})
	}
	for i := 0; i < 2; i++ {
		if err := q.EnqueueBatch(context.Background(), items); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM arl.jobs WHERE project_id=$1", db.Project).Scan(&count)
	if count != 450 {
		t.Fatal(count)
	}
}

func TestTransitionOutboxRollbackAndInitialDocumentReplay(t *testing.T) {
	db := testutil.DB(t)
	q, _ := New(context.Background(), db, "", "", 2)
	ctx := context.Background()
	_, err := q.Enqueue(ctx, "prepare", M{}, "initial", Doc{"campaigns", "atomic", M{"status": "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Update(ctx, "campaigns", "atomic", func(m M) error { m["status"] = "running"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.Enqueue(ctx, "prepare", M{}, "initial", Doc{"campaigns", "atomic", M{"status": "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := db.Get(ctx, "campaigns", "atomic")
	if c["status"] != "running" {
		t.Fatal("retry overwrote campaign state")
	}
	_, err = q.Enqueue(ctx, "other", M{}, "collision")
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.MutateAndEnqueue(ctx, "campaigns", "atomic", func(m M) (Item, error) {
		m["status"] = "cancelling"
		return Item{Kind: "cancel", Key: "collision", Payload: M{}}, nil
	})
	if err == nil {
		t.Fatal("conflicting outbox key accepted")
	}
	c, _ = db.Get(ctx, "campaigns", "atomic")
	if c["status"] != "running" {
		t.Fatal("state committed without continuation")
	}
	_, err = q.MutateAndEnqueue(ctx, "campaigns", "atomic", func(m M) (Item, error) {
		m["status"] = "cancelling"
		return Item{Kind: "cancel", Key: "valid-cancel", Payload: M{"campaign_id": "atomic"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = db.Get(ctx, "campaigns", "atomic")
	var count int
	err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM arl.jobs WHERE project_id=$1 AND dedup_key='valid-cancel'", db.Project).Scan(&count)
	if err != nil || count != 1 || c["status"] != "cancelling" {
		t.Fatal(c, count, err)
	}
}

func TestPendingHonorsCapacityAndRecoversExpiredLease(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q, _ := New(ctx, db, "", "", 2)
	err := db.Put(ctx, "campaigns", "busy", M{"status": "running", "active_count": 1, "plan": M{"protocol": M{"concurrency": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.Enqueue(ctx, "trial.run", M{"campaign_id": "busy", "trial_id": "t"}, "busy-trial")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := q.Pending(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatal("full campaign dispatched", pending, err)
	}
	_, err = db.Update(ctx, "campaigns", "busy", func(c M) error { c["active_count"] = 0; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pending, err = q.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0] != id {
		t.Fatal("available work omitted", pending, err)
	}
	_, err = q.Claim(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, "UPDATE arl.jobs SET lease_until=now()-interval '1 second' WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Update(ctx, "campaigns", "busy", func(c M) error { c["active_count"] = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	pending, err = q.Pending(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0] != id {
		t.Fatal("expired local lease not recoverable", pending, err)
	}
}

func TestMalformedQueueReference(t *testing.T) {
	if _, err := JobID("not-json"); err == nil {
		t.Fatal("invalid reference accepted")
	}
	if id, err := JobID(string(domain.JSON(M{"job_id": "job_1"}))); err != nil || id != "job_1" {
		t.Fatal(id, err)
	}
}
