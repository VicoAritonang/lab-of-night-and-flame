package testutil

import (
	"context"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/config"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"net/url"
	"os"
	"testing"
	"time"
)

// DB never falls back to NEON_DB_URL: integration tests require an explicitly isolated database.
func DB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("ARL_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set ARL_TEST_DB_URL to an initialized isolated PostgreSQL database")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test URL")
	}
	if !domain.Has([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname()) && os.Getenv("ARL_ALLOW_REMOTE_TEST_DB") != "true" {
		t.Fatal("remote integration database requires ARL_ALLOW_REMOTE_TEST_DB=true")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Ready(ctx); err != nil {
		db.Pool.Close()
		t.Fatal("initialize isolated database using sql/init.sql before tests")
	}
	project := domain.ID("test")
	if _, err = db.Pool.Exec(ctx, "INSERT INTO arl.projects(id,name) VALUES($1,'Go integration test')", project); err != nil {
		t.Fatal(err)
	}
	db = db.Scope(project)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := db.Pool.Begin(ctx)
		if err == nil {
			defer tx.Rollback(ctx)
			queries := []string{"DELETE FROM arl.trace_events WHERE trace_id IN (SELECT id FROM arl.traces WHERE project_id=$1)", "DELETE FROM arl.traces WHERE project_id=$1", "DELETE FROM arl.sessions WHERE project_id=$1", "DELETE FROM arl.stream_events WHERE project_id=$1", "DELETE FROM arl.artifacts WHERE project_id=$1", "DELETE FROM arl.uploads WHERE project_id=$1", "DELETE FROM arl.provider_limits WHERE project_id=$1", "DELETE FROM arl.jobs WHERE project_id=$1", "DELETE FROM arl.documents WHERE project_id=$1", "DELETE FROM arl.audit WHERE project_id=$1", "DELETE FROM arl.projects WHERE id=$1"}
			for _, q := range queries {
				if _, e := tx.Exec(ctx, q, project); e != nil {
					t.Errorf("test cleanup failed")
					break
				}
			}
			_ = tx.Commit(ctx)
		}
		db.Pool.Close()
	})
	return db
}
func Config() config.Config {
	return config.Config{DBMax: 8, Concurrency: 6, MaxHTTP: 48, MaxWebSockets: 100, JobWait: 20 * time.Second, Region: "ap-southeast-1", LocalURL: "http://127.0.0.1:1/v1", Mode: "sync"}
}
