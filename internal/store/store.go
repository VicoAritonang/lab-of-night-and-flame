package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"sort"
	"time"
)

type M = domain.M

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

type Store struct {
	Pool    *pgxpool.Pool
	Project string
}

func Open(ctx context.Context, dsn string, max int) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	cfg.MaxConns = int32(max)
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 20 * time.Minute
	cfg.MaxConnIdleTime = 90 * time.Second
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	// Neon transaction pooler: avoid connection-bound prepared statements.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot create PostgreSQL pool")
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, errors.New("cannot reach PostgreSQL; verify NEON_DB_URL and TLS")
	}
	return &Store{p, "default"}, nil
}
func (s *Store) Scope(project string) *Store {
	if project == "" {
		project = s.Project
	}
	if project == "" {
		project = "default"
	}
	return &Store{s.Pool, project}
}
func (s *Store) Ready(ctx context.Context) error {
	var v int
	return s.Pool.QueryRow(ctx, "SELECT max(version) FROM arl.schema_versions").Scan(&v)
}
func (s *Store) Get(ctx context.Context, kind, id string) (M, error) {
	var m M
	err := s.Pool.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3", s.Project, kind, id).Scan(&m)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}
func (s *Store) List(ctx context.Context, kind string, filter M, limit int) ([]M, error) {
	if filter == nil {
		filter = M{}
	}
	if limit <= 0 {
		limit = 200000
	}
	if limit > 2000000 {
		limit = 2000000
	}
	rows, err := s.Pool.Query(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND data @> $3::jsonb ORDER BY created_at,id LIMIT $4", s.Project, kind, domain.JSONB(filter), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		m := M{}
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) Count(ctx context.Context, kind string, filter M) (int, error) {
	if filter == nil {
		filter = M{}
	}
	var n int
	err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM arl.documents WHERE project_id=$1 AND kind=$2 AND data @> $3::jsonb", s.Project, kind, domain.JSONB(filter)).Scan(&n)
	return n, err
}
func (s *Store) Put(ctx context.Context, kind, id string, m M) error {
	m = domain.Copy(m)
	m["id"] = id
	_, err := s.Pool.Exec(ctx, `INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4) ON CONFLICT(project_id,kind,id) DO UPDATE SET data=EXCLUDED.data,version=arl.documents.version+1,updated_at=now()`, s.Project, kind, id, domain.JSONB(m))
	return err
}
func (s *Store) Create(ctx context.Context, kind, id string, m M) error {
	m = domain.Copy(m)
	m["id"] = id
	tag, err := s.Pool.Exec(ctx, "INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", s.Project, kind, id, domain.JSONB(m))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return err
}
func (s *Store) Update(ctx context.Context, kind, id string, fn func(M) error) (M, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var m M
	err = tx.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3 FOR UPDATE", s.Project, kind, id).Scan(&m)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = fn(m); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "UPDATE arl.documents SET data=$4,version=version+1,updated_at=now() WHERE project_id=$1 AND kind=$2 AND id=$3", s.Project, kind, id, domain.JSONB(m))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return m, nil
}
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3", s.Project, kind, id)
	return err
}
func (s *Store) Batch(ctx context.Context, kind string, docs []M) error {
	// Bounded transaction batches avoid giant wire buffers on the small gateway.
	for start := 0; start < len(docs); start += 200 {
		end := domain.Min(start+200, len(docs))
		batch := &pgx.Batch{}
		for _, m := range docs[start:end] {
			batch.Queue("INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", s.Project, kind, domain.Str(m["id"]), domain.JSONB(m))
		}
		res := s.Pool.SendBatch(ctx, batch)
		if err := res.Close(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Emit(ctx context.Context, channel string, event M) error {
	_, err := s.Pool.Exec(ctx, "INSERT INTO arl.stream_events(project_id,channel,event) VALUES($1,$2,$3)", s.Project, channel, domain.JSONB(event))
	return err
}
func (s *Store) Events(ctx context.Context, channel string, after int64, limit int) ([]M, int64, error) {
	rows, err := s.Pool.Query(ctx, "SELECT ordinal,event FROM arl.stream_events WHERE project_id=$1 AND channel=$2 AND ordinal>$3 ORDER BY ordinal LIMIT $4", s.Project, channel, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		m := M{}
		var n int64
		if err := rows.Scan(&n, &m); err != nil {
			return nil, after, err
		}
		m["ordinal"] = n
		after = n
		out = append(out, m)
	}
	return out, after, rows.Err()
}
func (s *Store) Audit(ctx context.Context, action, resource string, detail M) error {
	_, err := s.Pool.Exec(ctx, "INSERT INTO arl.audit(project_id,ts,actor,action,resource,detail) VALUES($1,$2,'owner',$3,$4,$5)", s.Project, domain.Now(), action, resource, domain.JSONB(detail))
	return err
}
func Order(rows []M, key string, desc bool) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i][key], rows[j][key]
		var less bool
		if _, ok := a.(string); ok {
			less = domain.Str(a) < domain.Str(b)
		} else {
			less = domain.Num(a) < domain.Num(b)
		}
		if desc {
			return !less && fmt.Sprint(a) != fmt.Sprint(b)
		}
		return less
	})
}
