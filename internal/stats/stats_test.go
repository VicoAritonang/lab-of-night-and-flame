package stats

import (
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
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
func TestMultipleComparisonAdjustments(t *testing.T) {
	ps := []any{0.01, nil, 0.04, 0.03}
	holm := AdjustHolm(ps)
	if holm[1] != nil || math.Abs(holm[0].(float64)-0.03) > 1e-12 || math.Abs(holm[2].(float64)-0.06) > 1e-12 || math.Abs(holm[3].(float64)-0.06) > 1e-12 {
		t.Fatal(holm)
	}
	bh := AdjustBH(ps)
	if math.Abs(bh[0].(float64)-0.03) > 1e-12 || math.Abs(bh[2].(float64)-0.04) > 1e-12 || math.Abs(bh[3].(float64)-0.04) > 1e-12 {
		t.Fatal(bh)
	}
}
func TestPowerAnalysis(t *testing.T) {
	// Cohen (1988): h=0.2, alpha=.05 two-sided, power=.8 → n≈196.
	n := SampleSize(0.5, math.Pow(math.Sin(math.Asin(math.Sqrt(0.5))+0.1), 2), 0.05, 0.8)
	if n.(int) < 195 || n.(int) > 197 {
		t.Fatal(n)
	}
	mde := MinimumDetectable(0.5, 196, 0.05, 0.8).(float64)
	if mde < 0.09 || mde > 0.11 {
		t.Fatal(mde)
	}
	if math.Abs(normalQuantile(0.975)-1.959963984540054) > 1e-8 {
		t.Fatal("quantile")
	}
}
func TestAgreement(t *testing.T) {
	// Classic 2x2 example: 20 yes/yes, 5 yes/no, 10 no/yes, 15 no/no → κ=0.4.
	a, b := []string{}, []string{}
	add := func(n int, x, y string) {
		for i := 0; i < n; i++ {
			a, b = append(a, x), append(b, y)
		}
	}
	add(20, "y", "y")
	add(5, "y", "n")
	add(10, "n", "y")
	add(15, "n", "n")
	if k := CohenKappa(a, b).(float64); math.Abs(k-0.4) > 1e-12 {
		t.Fatal(k)
	}
	if alpha := KrippendorffAlpha([][]float64{{1, 1}, {0, 0}, {1, 1, 1}}, false).(float64); alpha != 1 {
		t.Fatal(alpha)
	}
	// Krippendorff (2011) nominal reliability-data example → α≈0.743.
	units := [][]float64{{1, 1, 1}, {2, 2, 3, 2}, {3, 3, 3, 3}, {3, 3, 3, 3}, {2, 2, 2, 2}, {1, 2, 3, 4}, {4, 4, 4, 4}, {1, 1, 2, 1}, {2, 2, 2, 2}, {5, 5, 5}, {1, 1}, {3}}
	if alpha := KrippendorffAlpha(units, false).(float64); math.Abs(alpha-0.743) > 1e-3 {
		t.Fatal(alpha)
	}
	if r := Spearman([]float64{1, 2, 3, 4}, []float64{10, 20, 30, 45}).(float64); r != 1 {
		t.Fatal(r)
	}
	if Pearson([]float64{1, 1, 1}, []float64{1, 2, 3}) != nil {
		t.Fatal("constant series has no correlation")
	}
}
