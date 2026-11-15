package stats

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"math"
	"reflect"
	"testing"
)

func TestWilsonReference(t *testing.T) {
	iv := Wilson(7, 10)
	if math.Abs(domain.Num(iv["low"])-0.3968) > 1e-4 || math.Abs(domain.Num(iv["high"])-0.8922) > 1e-4 {
		t.Fatal(iv)
	}
	if Wilson(0, 0)["estimate"] != nil {
		t.Fatal("missing denominator must be unknown")
	}
	z := Wilson(0, 20)
	if domain.Num(z["low"]) != 0 || domain.Num(z["high"]) < 0.15 {
		t.Fatal(z)
	}
}
func TestMcNemar(t *testing.T) {
	if math.Abs(domain.Num(McNemar(10, 2))-0.03857) > 1e-4 || McNemar(0, 0) != nil || domain.Num(McNemar(3, 3)) != 1 {
		t.Fatal("exact test reference mismatch")
	}
}
func TestPassK(t *testing.T) {
	for _, x := range []struct {
		n, c, k int
		want    float64
	}{{10, 3, 1, 0.3}, {5, 0, 2, 0}, {5, 4, 2, 1}, {5, 2, 3, 0.9}} {
		if math.Abs(domain.Num(PassK(x.n, x.c, x.k))-x.want) > 1e-9 {
			t.Fatal(x)
		}
	}
	if PassK(3, 1, 4) != nil {
		t.Fatal("incomplete repetitions must be unknown")
	}
}
func TestBootstrapDeterminism(t *testing.T) {
	xs := []float64{0, 1, 1, 0, 1, 1, 1, 0, 1, 1}
	a, b := Bootstrap(xs, 7), Bootstrap(xs, 7)
	if !reflect.DeepEqual(a, b) || domain.Num(a["low"]) >= 0.7 || domain.Num(a["high"]) <= 0.7 {
		t.Fatal(a, b)
	}
	c := Bootstrap([]float64{1, 1, 1, 1, 1}, 1)
	if domain.Num(c["low"]) != 1 || domain.Num(c["high"]) != 1 {
		t.Fatal(c)
	}
	if Bootstrap(nil, 1)["estimate"] != nil {
		t.Fatal("empty bootstrap")
	}
}
func TestPairedDifference(t *testing.T) {
	iv := Paired([]float64{0, 0, 1, 0, 1, 0, 0, 1}, []float64{1, 1, 1, 0, 1, 1, 0, 1}, 1)
	if domain.Num(iv["estimate"]) != 0.375 {
		t.Fatal(iv)
	}
}
func TestDistributionAndPareto(t *testing.T) {
	if Percentile([]float64{1, 2, 3, 4}, 0.5) != 2.5 || Summary(nil)["p50"] != nil {
		t.Fatal("empty/percentile")
	}
	p := Pareto([]M{{"id": "a", "x": 1., "y": 0.5}, {"id": "b", "x": 2., "y": 0.7}, {"id": "c", "x": 3., "y": 0.6}, {"id": "d", "x": 0.5, "y": 0.4}})
	if !reflect.DeepEqual(p, []string{"d", "a", "b"}) {
		t.Fatal(p)
	}
}
