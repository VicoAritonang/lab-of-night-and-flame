import math

from arl import stats


def test_wilson_reference_values():
    iv = stats.wilson(7, 10)
    assert math.isclose(iv.low, 0.3968, abs_tol=1e-4) and math.isclose(iv.high, 0.8922, abs_tol=1e-4)
    zero = stats.wilson(0, 20)
    assert zero.low == 0 and 0.15 < zero.high < 0.17  # zero observed failures is not zero risk
    assert stats.wilson(0, 0).estimate is None


def test_mcnemar_exact():
    assert math.isclose(stats.mcnemar_exact(10, 2), 0.03857, abs_tol=1e-4)
    assert stats.mcnemar_exact(0, 0) is None
    assert stats.mcnemar_exact(3, 3) == 1.0


def test_pass_at_k_unbiased():
    assert math.isclose(stats.pass_at_k(10, 3, 1), 0.3)
    assert stats.pass_at_k(5, 0, 2) == 0.0
    assert stats.pass_at_k(5, 4, 2) == 1.0
    assert math.isclose(stats.pass_at_k(5, 2, 3), 0.9)
    assert stats.pass_at_k(3, 1, 4) is None


def test_bootstrap_deterministic_and_degenerate():
    xs = [0, 1, 1, 0, 1, 1, 1, 0, 1, 1]
    a, b = stats.bootstrap_mean(xs, seed=7), stats.bootstrap_mean(xs, seed=7)
    assert a == b and a.low < 0.7 < a.high
    const = stats.bootstrap_mean([1.0] * 5)
    assert const.low == const.high == 1.0
    assert stats.bootstrap_mean([]).estimate is None


def test_paired_diff_sign():
    base = [0, 0, 1, 0, 1, 0, 0, 1]
    cand = [1, 1, 1, 0, 1, 1, 0, 1]
    d = stats.paired_bootstrap_diff(base, cand, seed=1)
    assert d.estimate == 0.375 and d.low > 0


def test_percentiles_and_summary_empty():
    assert stats.percentile([1, 2, 3, 4], 0.5) == 2.5
    s = stats.summarize([])
    assert s["n"] == 0 and s["p50"] is None and s["mean"] is None  # unknown, never zero


def test_pareto():
    pts = [("a", 1.0, 0.5), ("b", 2.0, 0.7), ("c", 3.0, 0.6), ("d", 0.5, 0.4)]
    assert stats.pareto_frontier(pts) == ["d", "a", "b"]
