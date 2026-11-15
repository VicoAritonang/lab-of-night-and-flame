package stats

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"math"
	"math/rand"
	"sort"
)

type M = domain.M

func Interval(est, lo, hi any, n int, method string) M {
	return M{"estimate": est, "low": lo, "high": hi, "n": n, "method": method}
}
func Wilson(k, n int) M {
	if n == 0 {
		return Interval(nil, nil, nil, 0, "wilson_95")
	}
	p, z := float64(k)/float64(n), 1.959963984540054
	d := 1 + z*z/float64(n)
	mid := (p + z*z/(2*float64(n))) / d
	half := z * math.Sqrt(p*(1-p)/float64(n)+z*z/(4*float64(n*n))) / d
	lo, hi := math.Max(0, mid-half), math.Min(1, mid+half)
	if k == 0 {
		lo = 0
	}
	if k == n {
		hi = 1
	}
	return Interval(p, lo, hi, n, "wilson_95")
}
func Percentile(xs []float64, p float64) any {
	if len(xs) == 0 {
		return nil
	}
	v := append([]float64{}, xs...)
	sort.Float64s(v)
	at := p * float64(len(v)-1)
	i := int(at)
	if i == len(v)-1 {
		return v[i]
	}
	return v[i] + (v[i+1]-v[i])*(at-float64(i))
}
func Mean(xs []float64) any {
	if len(xs) == 0 {
		return nil
	}
	sum := 0.
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
func Bootstrap(xs []float64, seed int64) M {
	if len(xs) == 0 {
		return Interval(nil, nil, nil, 0, "bootstrap_95")
	}
	r := rand.New(rand.NewSource(seed))
	means := make([]float64, 2000)
	for i := range means {
		for range xs {
			means[i] += xs[r.Intn(len(xs))]
		}
		means[i] /= float64(len(xs))
	}
	return Interval(Mean(xs), Percentile(means, 0.025), Percentile(means, 0.975), len(xs), "bootstrap_95")
}
func Paired(a, b []float64, seed int64) M {
	if len(a) != len(b) || len(a) == 0 {
		return Interval(nil, nil, nil, 0, "paired_bootstrap_95")
	}
	d := make([]float64, len(a))
	for i := range a {
		d[i] = b[i] - a[i]
	}
	iv := Bootstrap(d, seed)
	iv["method"] = "paired_bootstrap_95"
	return iv
}
func McNemar(w, l int) any {
	n := w + l
	if n == 0 {
		return nil
	}
	small := domain.Min(w, l)
	sum := 0.
	ln, _ := math.Lgamma(float64(n + 1))
	for k := 0; k <= small; k++ {
		lk, _ := math.Lgamma(float64(k + 1))
		lr, _ := math.Lgamma(float64(n - k + 1))
		sum += math.Exp(ln - lk - lr - float64(n)*math.Ln2)
	}
	return math.Min(1, 2*sum)
}
func PassK(n, c, k int) any {
	if k > n || k < 1 {
		return nil
	}
	if n-c < k {
		return 1.
	}
	p := 1.
	for i := 0; i < k; i++ {
		p *= float64(n-c-i) / float64(n-i)
	}
	return 1 - p
}
func Summary(xs []float64) M {
	m := M{"n": len(xs), "mean": Mean(xs)}
	for key, p := range map[string]float64{"min": 0, "p50": 0.5, "p90": 0.9, "p95": 0.95, "p99": 0.99, "max": 1} {
		m[key] = Percentile(xs, p)
	}
	return m
}
func Histogram(xs []float64) M {
	if len(xs) == 0 {
		return M{"edges": []float64{}, "counts": []int{}}
	}
	max := domain.Num(Percentile(xs, 1))
	if max <= 0 {
		max = 1
	}
	edges := make([]float64, 13)
	counts := make([]int, 12)
	for i := range edges {
		edges[i] = max * float64(i) / 12
	}
	for _, x := range xs {
		i := domain.Min(11, int(x/max*12))
		if i < 0 {
			i = 0
		}
		counts[i]++
	}
	return M{"edges": edges, "counts": counts}
}
func Pareto(points []M) []string {
	out := []string{}
	for _, p := range points {
		dominated := false
		for _, q := range points {
			if domain.Num(q["x"]) <= domain.Num(p["x"]) && domain.Num(q["y"]) >= domain.Num(p["y"]) && (domain.Num(q["x"]) < domain.Num(p["x"]) || domain.Num(q["y"]) > domain.Num(p["y"])) {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, domain.Str(p["id"]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		get := func(id string) float64 {
			for _, p := range points {
				if p["id"] == id {
					return domain.Num(p["x"])
				}
			}
			return 0
		}
		return get(out[i]) < get(out[j])
	})
	return out
}
