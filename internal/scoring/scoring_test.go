package scoring

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"math"
	"testing"
)

func TestScorerContracts(t *testing.T) {
	cases := []struct {
		key, out string
		expected any
		params   M
		pass     bool
	}{
		{"exact_match", " RED ", "red", M{}, true}, {"exact_match", "RED", "red", M{"case_sensitive": true}, false},
		{"normalized_match", "<think>ignore me</think>The Blue CAR!", "blue car", M{}, true},
		{"contains", "two and three", []any{"two", "three"}, M{"mode": "all"}, true},
		{"regex", "Answer: 42", nil, M{"pattern": "[0-9]+"}, true},
		{"numeric", "The subtotal is 12; Answer: $1,234.50", 1234.5, M{"abs_tol": 0.01}, true},
		{"choice", "The answer is **B**.", "B", M{}, true},
		{"json_valid", "{} ", nil, M{}, true}, {"json_schema", `{"a":1}`, nil, M{"schema": M{"type": "object", "required": []string{"a"}}}, true},
		{"json_fields", `{"name":"acme","total":12.001}`, M{"name": "Acme", "total": 12.}, M{"numeric_tolerance": 0.01}, true},
		{"token_f1", "the red large car", "red car", M{"threshold": 0.7}, true},
		{"similarity", "hella", "hello", M{"threshold": 0.8}, true},
		{"bbox_iou", "[0,0,10,10]", []any{0., 0., 10., 10.}, M{}, true},
	}
	for _, c := range cases {
		t.Run(c.key+c.out, func(t *testing.T) {
			v := Run(M{"key": c.key, "params": c.params}, c.out, c.expected, M{})
			if (v.Verdict == "pass") != c.pass {
				t.Fatalf("%+v", v)
			}
		})
	}
}
func TestMissingAndEmpty(t *testing.T) {
	if Run(M{"key": "exact_match"}, "", nil, M{}).Verdict != "not_applicable" {
		t.Fatal("missing reference")
	}
	if Run(M{"key": "exact_match"}, "", "x", M{}).Verdict != "fail" {
		t.Fatal("empty output")
	}
}
func TestAutoAndInvalidRegex(t *testing.T) {
	v := Run(M{"key": "auto"}, "4", 4., M{"scorer": "numeric"})
	if v.Verdict != "pass" {
		t.Fatal(v)
	}
	if Validate(M{"key": "regex", "params": M{"pattern": "["}}) == nil {
		t.Fatal("invalid regex accepted")
	}
	if Validate(M{"key": "json_schema", "params": M{}}) == nil {
		t.Fatal("missing schema")
	}
}
func TestUnicodeAndOverlap(t *testing.T) {
	v := Run(M{"key": "token_f1"}, "猫 猫 車", "猫 車", M{})
	if v.Score == nil || math.Abs(*v.Score-0.8) > 1e-9 {
		t.Fatal(v)
	}
	if domain.Num(0) != 0 {
		t.Fatal("numeric")
	}
}
