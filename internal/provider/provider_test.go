package provider

import (
	"context"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/testutil"
	"strings"
	"testing"
)

func TestReferenceCannotEnterRealProviderPayload(t *testing.T) {
	p := Payload(Request{Provider: "openai", Model: "test", Messages: []M{{"role": "user", "content": "question"}}, Params: M{"max_tokens": 30}, Reference: "PRIVATE_EXPECTATION_CANARY"})
	if strings.Contains(string(domain.JSON(p)), "PRIVATE_EXPECTATION_CANARY") || p["max_completion_tokens"] != 30 {
		t.Fatal(p)
	}
}
func TestProviderSpecificParameters(t *testing.T) {
	p := Payload(Request{Provider: "openrouter", Model: "router", Stream: true, Params: M{"max_tokens": 12, "reasoning_effort": "high", "json_mode": true}})
	if domain.Obj(p["reasoning"])["effort"] != "high" || p["usage"] == nil || p["stream_options"] != nil {
		t.Fatal(p)
	}
}

func TestGatewayUsesWorkerAvailabilityWithoutProviderSecrets(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	cfg := testutil.Config()
	cfg.Mode = "async"
	worker := New(db, cfg)
	const secret = "worker-only-test-secret"
	t.Setenv("OPENAI_API_KEY", secret)
	if err := worker.PublishStatus(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	cfg.Mode = "sync"
	gateway := New(db, cfg)
	found := false
	for _, item := range gateway.Status(ctx) {
		if item["name"] == "openai" {
			found = true
			if item["configured"] != true {
				t.Fatal("gateway ignored worker configuration", item)
			}
		}
	}
	if !found {
		t.Fatal("provider missing")
	}
	snapshot, err := db.Get(ctx, "settings", "worker_providers")
	if err != nil || strings.Contains(string(domain.JSON(snapshot)), secret) {
		t.Fatal("secret was persisted", err)
	}
	for _, item := range worker.Status(ctx) {
		if item["name"] == "openai" && item["configured"] != false {
			t.Fatal("worker trusted stale snapshot after key removal")
		}
	}
}

func TestFakeRuntimeOracleAndRetry(t *testing.T) {
	req := Request{Provider: "fake", Model: "fake/oracle-90", SampleKey: "test", Messages: []M{{"role": "user", "content": []M{{"type": "text", "text": "2+2?"}}}}, Params: M{"seed": 0}, Reference: 4., Attempt: 1}
	a, err := Fake(context.Background(), req)
	if err != nil || a["cost_usd"] != 0. || a["text"] == nil {
		t.Fatal(a, err)
	}
	b, _ := Fake(context.Background(), req)
	if a["text"] != b["text"] {
		t.Fatal("fake output nondeterministic")
	}
}
