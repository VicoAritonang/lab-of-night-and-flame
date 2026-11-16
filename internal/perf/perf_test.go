package perf

import "testing"

func level(c int, rps, p95, errRate float64) M {
	return M{"concurrency": c, "requests_per_s": rps, "error_rate": errRate, "latency_ms": M{"p95": p95}}
}

func TestSaturationStopsWhereLatencyDegrades(t *testing.T) {
	s := Saturation([]M{level(1, 2, 1000, 0), level(4, 7, 1300, 0), level(8, 9, 2400, 0), level(16, 9.5, 5000, 0.2)})
	if s["concurrency"] != 4 {
		t.Fatal("expected the last level within 1.5x of sequential p95", s)
	}
	if s := Saturation([]M{level(1, 2, 1000, 0.5)}); s["concurrency"] != nil {
		t.Fatal("an erroring first level has no sweet spot", s)
	}
}
