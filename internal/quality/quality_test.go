package quality

import (
	"testing"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
)

func TestValidation(t *testing.T) {
	f, err := ValidateFeedback(M{"name": "thumbs", "value": true})
	if err != nil || f["value"] != 1. || f["source"] != "user" {
		t.Fatal(f, err)
	}
	for _, bad := range []M{{"name": ""}, {"name": "x"}, {"name": "x", "value": "high"}, {"name": "x", "value": 1, "source": "auto"}} {
		if _, err := ValidateFeedback(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	ev, err := ValidateEvaluator(M{"name": "judge", "type": "llm_judge", "config": M{"model": "m", "rubric": "be right"}})
	if err != nil || ev["sample_rate"] != 1. || domain.Obj(ev["config"])["provider"] != "openrouter" || ev["feedback_name"] != "judge" {
		t.Fatal(ev, err)
	}
	for _, bad := range []M{{"name": "x", "type": "llm_judge", "config": M{}}, {"name": "x", "type": "checks", "config": M{"checks": M{"nope": 1}}}, {"name": "x", "type": "scorer", "config": M{"key": "llm_judge"}}, {"name": "x", "type": "checks", "sample_rate": 2}} {
		if _, err := ValidateEvaluator(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if _, err := ValidateAlert(M{"name": "a", "metric": "feedback_mean", "threshold": 0.5}); err == nil {
		t.Fatal("feedback_mean without feedback_name accepted")
	}
	if _, err := ValidateAlert(M{"name": "a", "metric": "cost_usd", "threshold": 1, "webhook_url": "file:///etc/passwd"}); err == nil {
		t.Fatal("non-http webhook accepted")
	}
	q, err := ValidateQueue(M{"name": "q", "rubric": []any{M{"name": "tone", "kind": "scale"}, M{"name": "kind", "kind": "label", "options": []any{"a", "b"}}}})
	if err != nil || domain.Maps(q["rubric"])[0]["max"] != 5. || q["required"] != 1 {
		t.Fatal(q, err)
	}
}

func TestSamplingIsDeterministic(t *testing.T) {
	hits := 0
	for i := 0; i < 2000; i++ {
		id := domain.ID("trace")
		a := sampled(id, "ev", 0.25)
		if a != sampled(id, "ev", 0.25) {
			t.Fatal("same trace sampled differently")
		}
		if a {
			hits++
		}
	}
	if hits < 400 || hits > 600 {
		t.Fatal("sample rate off", hits)
	}
}

func TestJudgeAgreementOnBinaryCriterion(t *testing.T) {
	one, zero := 1., 0.
	ratings := []rating{
		{trace: "t1", author: "a", source: "human", value: &one}, {trace: "t1", author: "b", source: "human", value: &one}, {trace: "t1", author: "evaluator", source: "auto", value: &one},
		{trace: "t2", author: "a", source: "human", value: &zero}, {trace: "t2", author: "b", source: "human", value: &zero}, {trace: "t2", author: "evaluator", source: "auto", value: &one},
	}
	out := criterionAgreement(M{"name": "correct", "kind": "binary"}, ratings)
	judge := domain.Obj(out["judge"])
	if out["alpha"] != 1. || out["kappa"] != 1. || judge["accuracy"] != 0.5 || domain.Obj(judge["confusion"])["fn"] != 1 {
		t.Fatal(out)
	}
}
