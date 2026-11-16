package store_test

import (
	"context"
	"errors"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	"go.vicoworks.com/agent-reliability-lab/internal/testutil"
	"sync"
	"testing"
)

func TestAtomicBudgetUnderConcurrentWorkers(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	if err := db.Put(ctx, "campaigns", "budget", domain.M{"reserved": 0., "cap": 1.}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.Update(ctx, "campaigns", "budget", func(m domain.M) error {
				if domain.Num(m["reserved"])+0.1 > domain.Num(m["cap"])+1e-10 {
					return store.ErrConflict
				}
				m["reserved"] = domain.Num(m["reserved"]) + 0.1
				return nil
			})
			if err != nil && !errors.Is(err, store.ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	result, _ := db.Get(ctx, "campaigns", "budget")
	if value := domain.Num(result["reserved"]); value > 1.000000001 || value < 0.99 {
		t.Fatal(value)
	}
}
func TestJSONBTenantIsolation(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	if err := db.Put(ctx, "items", "one", domain.M{"nested": domain.M{"value": 1}, "array": []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	m, err := db.Get(ctx, "items", "one")
	if err != nil || domain.Int(domain.Obj(m["nested"])["value"]) != 1 {
		t.Fatal(m, err)
	}
	if _, err = db.Scope("default").Get(ctx, "items", "one"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("project data leaked", err)
	}
}
