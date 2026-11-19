package queue

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"log/slog"
	"sync"
	"time"
)

type M = domain.M

var ErrDefer = errors.New("defer job until capacity is available")

type Job struct {
	ID, Project, Kind, Token string
	Payload                  M
	Attempts                 int
}
type Doc struct {
	Kind, ID string
	Data     M
}
type Item struct {
	Kind, Key string
	Payload   M
}
type Queue struct {
	DB *store.Store
	// Remote hands job references to the split deployment's workers (SQS);
	// nil in the all-in-one mode, where RunLocal polls PostgreSQL instead.
	Remote      Sender
	URL         string
	Concurrency int
}

// Sender delivers job-reference message bodies and returns the indexes that
// were accepted. The SQS implementation is left out with -tags arl_nosqs.
type Sender interface {
	Send(ctx context.Context, bodies []string) ([]int, error)
}

// SendFunc adapts a function to Sender.
type SendFunc func(ctx context.Context, bodies []string) ([]int, error)

func (f SendFunc) Send(ctx context.Context, bodies []string) ([]int, error) { return f(ctx, bodies) }

func New(ctx context.Context, db *store.Store, url, region string, n int) (*Queue, error) {
	q := &Queue{DB: db, URL: url, Concurrency: n}
	if url != "" {
		remote, err := newSQS(ctx, url, region)
		if err != nil {
			return nil, err
		}
		q.Remote = remote
	}
	return q, nil
}
func (q *Queue) Scope(project string) *Queue {
	return &Queue{q.DB.Scope(project), q.Remote, q.URL, q.Concurrency}
}
func (q *Queue) EnqueueBatch(ctx context.Context, items []Item) error {
	for start := 0; start < len(items); start += 200 {
		end := domain.Min(start+200, len(items))
		batch := &pgx.Batch{}
		for _, item := range items[start:end] {
			batch.Queue("INSERT INTO arl.jobs(id,project_id,kind,payload,dedup_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", domain.ID("job"), q.DB.Project, item.Kind, domain.JSONB(item.Payload), item.Key)
		}
		results := q.DB.Pool.SendBatch(ctx, batch)
		if err := results.Close(); err != nil {
			return err
		}
	}
	return nil
}
func (q *Queue) Enqueue(ctx context.Context, kind string, payload M, key string, docs ...Doc) (string, error) {
	tx, err := q.DB.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	for _, d := range docs {
		d.Data = domain.Copy(d.Data)
		d.Data["id"] = d.ID
		_, err = tx.Exec(ctx, "INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4) ON CONFLICT(project_id,kind,id) DO NOTHING", q.DB.Project, d.Kind, d.ID, domain.JSONB(d.Data))
		if err != nil {
			return "", err
		}
	}
	id := domain.ID("job")
	var storedKind string
	var storedPayload M
	err = tx.QueryRow(ctx, "INSERT INTO arl.jobs(id,project_id,kind,payload,dedup_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(project_id,dedup_key) DO UPDATE SET dedup_key=EXCLUDED.dedup_key RETURNING id,kind,payload", id, q.DB.Project, kind, domain.JSONB(payload), key).Scan(&id, &storedKind, &storedPayload)
	if err != nil {
		return "", err
	}
	if storedKind != kind || domain.Hash(storedPayload) != domain.Hash(payload) {
		return "", store.ErrConflict
	}
	return id, tx.Commit(ctx)
}

// MutateAndEnqueue commits a state transition and its durable continuation together.
func (q *Queue) MutateAndEnqueue(ctx context.Context, kind, id string, fn func(M) (Item, error)) (M, error) {
	tx, err := q.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var data M
	err = tx.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3 FOR UPDATE", q.DB.Project, kind, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	item, err := fn(data)
	if err != nil {
		return nil, err
	}
	var storedKind string
	var storedPayload M
	err = tx.QueryRow(ctx, "INSERT INTO arl.jobs(id,project_id,kind,payload,dedup_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(project_id,dedup_key) DO UPDATE SET dedup_key=EXCLUDED.dedup_key RETURNING kind,payload", domain.ID("job"), q.DB.Project, item.Kind, domain.JSONB(item.Payload), item.Key).Scan(&storedKind, &storedPayload)
	if err != nil {
		return nil, err
	}
	if storedKind != item.Kind || domain.Hash(storedPayload) != domain.Hash(item.Payload) {
		return nil, store.ErrConflict
	}
	_, err = tx.Exec(ctx, "UPDATE arl.documents SET data=$4,version=version+1,updated_at=now() WHERE project_id=$1 AND kind=$2 AND id=$3", q.DB.Project, kind, id, domain.JSONB(data))
	if err != nil {
		return nil, err
	}
	return data, tx.Commit(ctx)
}

func (q *Queue) Result(ctx context.Context, id string) (M, error) {
	var state string
	var result M
	var detail *string
	err := q.DB.Pool.QueryRow(ctx, "SELECT state,result,error FROM arl.jobs WHERE project_id=$1 AND id=$2", q.DB.Project, id).Scan(&state, &result, &detail)
	if err != nil {
		return nil, err
	}
	if state == "failed" {
		msg := "async job failed"
		if detail != nil {
			msg = *detail
		}
		return nil, errors.New(msg)
	}
	if state == "done" {
		if result == nil {
			result = M{}
		}
		return result, nil
	}
	return nil, nil
}
func (q *Queue) Wait(ctx context.Context, id string) (M, error) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		r, e := q.Result(ctx, id)
		if e != nil || r != nil {
			return r, e
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
func (q *Queue) Claim(ctx context.Context, id string) (*Job, error) {
	j := &Job{ID: id, Token: domain.ID("lease")}
	err := q.DB.Pool.QueryRow(ctx, `UPDATE arl.jobs SET state='running',attempts=attempts+1,lease_token=$2,lease_until=now()+interval '12 minutes',updated_at=now()
 WHERE id=$1 AND (state='queued' OR (state='running' AND lease_until<now())) RETURNING project_id,kind,payload,attempts`, id, j.Token).Scan(&j.Project, &j.Kind, &j.Payload, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}
func (q *Queue) Process(ctx context.Context, id string, fn func(context.Context, *Job) (M, error)) error {
	j, err := q.Claim(ctx, id)
	if err != nil || j == nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	heartbeatDone := make(chan struct{})
	defer func() { cancel(); close(stopped); <-heartbeatDone }()
	go func() {
		defer close(heartbeatDone)
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopped:
				return
			case <-t.C:
				tag, e := q.DB.Pool.Exec(ctx, "UPDATE arl.jobs SET lease_until=now()+interval '12 minutes',updated_at=now() WHERE id=$1 AND lease_token=$2 AND state='running'", id, j.Token)
				if e != nil || tag.RowsAffected() != 1 {
					cancel()
					return
				}
			}
		}
	}()
	var result M
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("worker panic; job remains auditable")
			}
		}()
		result, err = fn(ctx, j)
	}()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if errors.Is(err, ErrDefer) {
		_, e := q.DB.Pool.Exec(finishCtx, "UPDATE arl.jobs SET state='queued',attempts=GREATEST(0,attempts-1),lease_token=NULL,lease_until=NULL,sent_at=NULL,next_send_at=now()+interval '2 seconds' WHERE id=$1 AND lease_token=$2", id, j.Token)
		return e
	}
	if err != nil {
		state := "queued"
		if j.Attempts >= 4 {
			state = "failed"
		}
		_, e := q.DB.Pool.Exec(finishCtx, "UPDATE arl.jobs SET state=$3,error=$4,lease_token=NULL,lease_until=NULL,sent_at=NULL,next_send_at=now()+interval '5 seconds',updated_at=now() WHERE id=$1 AND lease_token=$2", id, j.Token, state, "async operation failed; inspect audit and job metadata")
		if e != nil {
			return e
		}
		if state == "failed" && domain.Has([]string{"campaign.prepare", "campaign.cancel", "campaign.rescore", "rescore.trial", "trial.run"}, j.Kind) {
			_, e = q.DB.Scope(j.Project).Update(finishCtx, "campaigns", domain.Str(j.Payload["campaign_id"]), func(c M) error {
				if domain.Has([]string{"queued", "running", "grading", "cancelling"}, domain.Str(c["status"])) {
					c["status"] = "requires_reconciliation"
					c["stop_reason"] = "async_job_failed"
					c["failed_job_id"] = id
				}
				return nil
			})
			if e != nil {
				return e
			}
		}
		return err
	}
	if result == nil {
		result = M{}
	}
	tag, e := q.DB.Pool.Exec(finishCtx, "UPDATE arl.jobs SET state='done',result=$3,error=NULL,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2", id, j.Token, domain.JSONB(result))
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return errors.New("job lease was lost")
	}
	return nil
}

// Avoid invoking Lambda repeatedly for trials that cannot enter a full campaign.
// Expired running leases must bypass admission filtering so crash recovery can run.
const admission = `(j.state='running' OR j.kind<>'trial.run' OR EXISTS (
 SELECT 1 FROM arl.documents c WHERE c.project_id=j.project_id AND c.kind='campaigns'
 AND c.id=j.payload->>'campaign_id' AND (
 (c.data->>'status'='running' AND COALESCE((c.data->>'active_count')::integer,0)<COALESCE((c.data#>>'{plan,protocol,concurrency}')::integer,1))
 OR c.data->>'status' IN ('cancelling','cancelled','completed'))))`

func (q *Queue) Pending(ctx context.Context, n int) ([]string, error) {
	rows, e := q.DB.Pool.Query(ctx, "SELECT j.id FROM arl.jobs j WHERE (j.state='queued' OR (j.state='running' AND j.lease_until<now())) AND j.next_send_at<=now() AND "+admission+" ORDER BY j.created_at LIMIT $1", n)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (q *Queue) DispatchOnce(ctx context.Context) error {
	if q.Remote == nil {
		return nil
	}
	tx, err := q.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT j.id FROM arl.jobs j WHERE ((j.state='queued' AND (j.sent_at IS NULL OR j.sent_at<now()-interval '10 minutes')) OR (j.state='running' AND j.lease_until<now())) AND j.next_send_at<=now() AND "+admission+" ORDER BY j.created_at FOR UPDATE SKIP LOCKED LIMIT 10")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e := rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, "UPDATE arl.jobs SET next_send_at=now()+interval '30 seconds' WHERE id=ANY($1)", ids)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	bodies := make([]string, len(ids))
	for i, id := range ids {
		bodies[i] = string(domain.JSONB(M{"job_id": id}))
	}
	sent, err := q.Remote.Send(ctx, bodies)
	if err != nil {
		return errors.New("SQS dispatch failed; outbox retained")
	}
	for _, i := range sent {
		if i >= 0 && i < len(ids) {
			_, err = q.DB.Pool.Exec(ctx, "UPDATE arl.jobs SET sent_at=now(),next_send_at=now()+interval '10 minutes',state=CASE WHEN state='running' AND lease_until<now() THEN 'queued' ELSE state END WHERE id=$1", ids[i])
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (q *Queue) Dispatch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := q.DispatchOnce(ctx); err != nil {
				slog.Warn("outbox dispatch unavailable")
			}
		}
	}
}
func (q *Queue) RunLocal(ctx context.Context, fn func(context.Context, *Job) (M, error)) error {
	slots := make(chan struct{}, q.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			ids, err := q.Pending(ctx, q.Concurrency)
			if err != nil {
				return err
			}
			for _, id := range ids {
				select {
				case slots <- struct{}{}:
					wg.Add(1)
					go func(id string) {
						defer wg.Done()
						defer func() { <-slots }()
						if e := q.Process(ctx, id, fn); e != nil {
							slog.Warn("async job will retry", "job_id", id)
						}
					}(id)
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
}
func JobID(body string) (string, error) {
	var ref struct {
		ID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(body), &ref); err != nil || ref.ID == "" {
		return "", errors.New("invalid job reference")
	}
	return ref.ID, nil
}
