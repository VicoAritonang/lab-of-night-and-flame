package stats

import (
	"math"
	"sort"
)

// AdjustHolm returns Holm–Bonferroni adjusted p-values (family-wise error
// control). nil entries (no test) stay nil and do not count as hypotheses.
func AdjustHolm(ps []any) []any {
	idx, vals := present(ps)
	m := len(vals)
	order := ranks(vals)
	adj := make([]float64, m)
	running := 0.
	for rank, i := range order {
		v := math.Min(1, float64(m-rank)*vals[i])
		running = math.Max(running, v)
		adj[i] = running
	}
	return scatter(ps, idx, adj)
}

// AdjustBH returns Benjamini–Hochberg adjusted p-values (false discovery rate).
func AdjustBH(ps []any) []any {
	idx, vals := present(ps)
	m := len(vals)
	order := ranks(vals)
	adj := make([]float64, m)
	running := 1.
	for rank := m - 1; rank >= 0; rank-- {
		i := order[rank]
		running = math.Min(running, vals[i]*float64(m)/float64(rank+1))
		adj[i] = math.Min(1, running)
	}
	return scatter(ps, idx, adj)
}

func present(ps []any) ([]int, []float64) {
	idx, vals := []int{}, []float64{}
	for i, p := range ps {
		if f, ok := p.(float64); ok && !math.IsNaN(f) {
			idx, vals = append(idx, i), append(vals, f)
		}
	}
	return idx, vals
}

func ranks(vals []float64) []int {
	order := make([]int, len(vals))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return vals[order[a]] < vals[order[b]] })
	return order
}

func scatter(ps []any, idx []int, adj []float64) []any {
	out := make([]any, len(ps))
	for k, i := range idx {
		out[i] = adj[k]
	}
	return out
}

// CohenH is the effect size between two proportions (0.2 small, 0.5 medium,
// 0.8 large).
func CohenH(p1, p2 float64) float64 {
	return 2*math.Asin(math.Sqrt(p2)) - 2*math.Asin(math.Sqrt(p1))
}

// normalQuantile is the inverse standard normal CDF (Acklam's algorithm,
// relative error below 1.2e-9).
func normalQuantile(p float64) float64 {
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	switch {
	case p <= 0:
		return math.Inf(-1)
	case p >= 1:
		return math.Inf(1)
	case p < 0.02425:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > 1-0.02425:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	}
	q := p - 0.5
	r := q * q
	return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q / (((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
}

// SampleSize returns the samples per arm needed to detect a pass-rate change
// from p1 to p2 with a two-sided test at level alpha and the given power
// (normal approximation with Cohen's h). Unpaired, so conservative for the
// paired designs the lab runs.
func SampleSize(p1, p2, alpha, power float64) any {
	h := math.Abs(CohenH(p1, p2))
	if h == 0 || alpha <= 0 || alpha >= 1 || power <= 0 || power >= 1 {
		return nil
	}
	z := normalQuantile(1-alpha/2) + normalQuantile(power)
	return int(math.Ceil(z * z / (h * h)))
}

// MinimumDetectable returns the smallest pass-rate change from baseline p that
// n samples per arm detect with the given alpha and power.
func MinimumDetectable(p float64, n int, alpha, power float64) any {
	if n < 1 || p < 0 || p > 1 {
		return nil
	}
	h := (normalQuantile(1-alpha/2) + normalQuantile(power)) / math.Sqrt(float64(n))
	// Solve CohenH(p, p2) = h for p2 above p (or below if p is near 1).
	up := math.Pow(math.Sin(math.Asin(math.Sqrt(p))+h/2), 2)
	if math.Asin(math.Sqrt(p))+h/2 > math.Pi/2 {
		down := math.Pow(math.Sin(math.Asin(math.Sqrt(p))-h/2), 2)
		return p - down
	}
	return up - p
}

// CohenKappa measures agreement between two raters on nominal labels, beyond
// chance. Pairs with an empty label on either side are skipped.
func CohenKappa(a, b []string) any {
	counts := map[string][2]float64{}
	agree, n := 0., 0.
	for i := range a {
		if i >= len(b) || a[i] == "" || b[i] == "" {
			continue
		}
		n++
		if a[i] == b[i] {
			agree++
		}
		ca := counts[a[i]]
		ca[0]++
		counts[a[i]] = ca
		cb := counts[b[i]]
		cb[1]++
		counts[b[i]] = cb
	}
	if n == 0 {
		return nil
	}
	expected := 0.
	for _, c := range counts {
		expected += (c[0] / n) * (c[1] / n)
	}
	if expected == 1 {
		return 1.
	}
	return (agree/n - expected) / (1 - expected)
}

// KrippendorffAlpha measures agreement among any number of raters with
// missing ratings. units[i] holds the ratings given to item i. interval=true
// uses squared differences (scales); false treats values as nominal labels.
func KrippendorffAlpha(units [][]float64, interval bool) any {
	delta := func(a, b float64) float64 {
		if interval {
			return (a - b) * (a - b)
		}
		if a == b {
			return 0
		}
		return 1
	}
	values := []float64{}
	observed, pairable := 0., 0.
	for _, u := range units {
		m := float64(len(u))
		if m < 2 {
			continue
		}
		pairable += m
		values = append(values, u...)
		for i := range u {
			for j := range u {
				if i != j {
					observed += delta(u[i], u[j]) / (m - 1)
				}
			}
		}
	}
	n := float64(len(values))
	if n < 2 {
		return nil
	}
	expected := 0.
	for i := range values {
		for j := range values {
			if i != j {
				expected += delta(values[i], values[j])
			}
		}
	}
	expected /= n * (n - 1)
	if expected == 0 {
		return 1.
	}
	return 1 - (observed/pairable)/expected
}

// Pearson correlation; nil when either side is constant.
func Pearson(x, y []float64) any {
	n := float64(len(x))
	if len(x) != len(y) || n < 3 {
		return nil
	}
	mx, my := 0., 0.
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx, my = mx/n, my/n
	sxy, sxx, syy := 0., 0., 0.
	for i := range x {
		sxy += (x[i] - mx) * (y[i] - my)
		sxx += (x[i] - mx) * (x[i] - mx)
		syy += (y[i] - my) * (y[i] - my)
	}
	if sxx == 0 || syy == 0 {
		return nil
	}
	return sxy / math.Sqrt(sxx*syy)
}

func rank(xs []float64) []float64 {
	order := ranks(xs)
	out := make([]float64, len(xs))
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && xs[order[j+1]] == xs[order[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[order[k]] = avg
		}
		i = j + 1
	}
	return out
}

// Spearman rank correlation (ties get average ranks).
func Spearman(x, y []float64) any {
	if len(x) != len(y) {
		return nil
	}
	return Pearson(rank(x), rank(y))
}
