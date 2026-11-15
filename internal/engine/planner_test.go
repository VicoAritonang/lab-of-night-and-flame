package engine

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"reflect"
	"testing"
)

func minimal() M {
	return M{"dataset_id": "ds", "targets": []M{{"provider": "fake", "model": "fake/echo"}}, "scorers": []M{{"id": "s", "key": "numeric"}}, "primary_scorer": "s"}
}
func TestPlanDefaultsAndFactorExpansion(t *testing.T) {
	raw := minimal()
	raw["targets"] = []M{{"provider": "fake", "model": "fake/echo"}, {"provider": "fake", "model": "fake/oracle-90"}}
	raw["prompts"] = []M{{"id": "a", "system": "{instructions}", "user": "{input}"}, {"id": "b", "system": "B", "user": "{input}"}}
	raw["params_grid"] = M{"temperature": []any{0., 1.}}
	p, err := Plan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(Arms(p)) != 8 || domain.Int(domain.Obj(p["protocol"])["concurrency"]) != 6 {
		t.Fatal(p)
	}
	if !reflect.DeepEqual(Arms(p), Arms(p)) {
		t.Fatal("unstable arm keys")
	}
}
func TestInvalidManifest(t *testing.T) {
	for _, change := range []func(M){func(m M) { m["unknown"] = 1 }, func(m M) { m["primary_scorer"] = "absent" }, func(m M) { m["protocol"] = M{"concurrency": 0} }, func(m M) { m["params_grid"] = M{"nonsense": []any{1}} }, func(m M) { m["scorers"] = []M{{"id": "s", "key": "unknown"}} }} {
		p := minimal()
		change(p)
		if _, err := Plan(p); err == nil {
			t.Fatal("invalid plan accepted", p)
		}
	}
}
func TestScheduleSeedAndPairing(t *testing.T) {
	arms := []M{{"key": "a"}, {"key": "b"}}
	samples := []M{{"id": "x"}, {"id": "y"}}
	proto := M{"seed": 7, "order": "interleaved", "repetitions": 3}
	a, b := Schedule(arms, samples, proto), Schedule(arms, samples, proto)
	if !reflect.DeepEqual(a, b) || len(a) != 12 {
		t.Fatal("scheduler nondeterministic")
	}
	seen := map[string]bool{}
	for _, row := range a {
		k := domain.Hash(row)
		if seen[k] {
			t.Fatal("duplicate scheduled trial")
		}
		seen[k] = true
	}
}
func TestPromptReferenceSeparation(t *testing.T) {
	if got := Render("{instructions}\n{input} {meta.language}", "instruction", "question", M{"language": "id", "expected": "secret"}); got != "instruction\nquestion id" {
		t.Fatal(got)
	}
}
