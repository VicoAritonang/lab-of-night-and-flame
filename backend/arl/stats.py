"""Statistics used by the analyzer. Pure functions, stdlib only, deterministic given a seed.

Every interval function states its method so reports can cite it (product.md §24).
"""
from __future__ import annotations

import math
import random
from dataclasses import dataclass
from typing import Sequence

Z95 = 1.959963984540054


@dataclass(frozen=True)
class Interval:
    estimate: float | None
    low: float | None
    high: float | None
    method: str
    n: int

    def as_dict(self) -> dict:
        return {"estimate": self.estimate, "low": self.low, "high": self.high, "method": self.method, "n": self.n}


def wilson(successes: int, n: int, z: float = Z95) -> Interval:
    """Wilson score interval for a binomial proportion. Well-behaved for small n and p near 0/1."""
    if n <= 0:
        return Interval(None, None, None, "wilson", 0)
    p = successes / n
    denom = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / denom
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / denom
    return Interval(p, max(0.0, centre - half), min(1.0, centre + half), "wilson", n)


def mean(xs: Sequence[float]) -> float | None:
    return sum(xs) / len(xs) if xs else None


def percentile(sorted_xs: Sequence[float], q: float) -> float | None:
    """Linear-interpolated percentile (numpy 'linear' method). Input MUST be sorted."""
    n = len(sorted_xs)
    if n == 0:
        return None
    if n == 1:
        return float(sorted_xs[0])
    pos = (n - 1) * q
    lo = math.floor(pos)
    hi = min(lo + 1, n - 1)
    frac = pos - lo
    return float(sorted_xs[lo] + (sorted_xs[hi] - sorted_xs[lo]) * frac)


def summarize(xs: Sequence[float]) -> dict:
    """Distribution summary for latency-like measurements; empty input yields None fields, never zeros."""
    s = sorted(x for x in xs if x is not None)
    return {
        "n": len(s),
        "mean": mean(s),
        "min": s[0] if s else None,
        "p50": percentile(s, 0.50),
        "p90": percentile(s, 0.90),
        "p95": percentile(s, 0.95),
        "p99": percentile(s, 0.99),
        "max": s[-1] if s else None,
    }


def bootstrap_mean(xs: Sequence[float], resamples: int = 2000, seed: int = 0, alpha: float = 0.05) -> Interval:
    """Percentile bootstrap CI of the mean."""
    n = len(xs)
    if n == 0:
        return Interval(None, None, None, f"bootstrap_percentile(B={resamples},seed={seed})", 0)
    est = sum(xs) / n
    if n == 1:
        return Interval(est, None, None, "single_observation", 1)
    rng = random.Random(seed)
    pop = list(xs)
    if min(pop) == max(pop):
        return Interval(est, est, est, f"bootstrap_percentile(B={resamples},seed={seed})", n)
    means = sorted(sum(rng.choices(pop, k=n)) / n for _ in range(resamples))
    return Interval(
        est,
        percentile(means, alpha / 2),
        percentile(means, 1 - alpha / 2),
        f"bootstrap_percentile(B={resamples},seed={seed})",
        n,
    )


def paired_bootstrap_diff(a: Sequence[float], b: Sequence[float], resamples: int = 2000, seed: int = 0) -> Interval:
    """CI for mean(b - a) over paired units (same sample). Resamples units, keeping pairs intact."""
    if len(a) != len(b):
        raise ValueError("paired inputs must have equal length")
    diffs = [y - x for x, y in zip(a, b)]
    iv = bootstrap_mean(diffs, resamples=resamples, seed=seed)
    return Interval(iv.estimate, iv.low, iv.high, "paired_" + iv.method, iv.n)


def mcnemar_exact(b_only: int, a_only: int) -> float | None:
    """Two-sided exact McNemar test on discordant pairs.

    b_only = pairs where candidate passed and baseline failed; a_only = the reverse.
    Returns p-value, or None when there are no discordant pairs (test undefined).
    """
    n = a_only + b_only
    if n == 0:
        return None
    k = min(a_only, b_only)
    # P(X <= k) for X ~ Binom(n, 0.5), doubled, capped at 1.
    tail = sum(math.comb(n, i) for i in range(k + 1)) / (2 ** n)
    return min(1.0, 2 * tail)


def pass_at_k(n: int, c: int, k: int) -> float | None:
    """Unbiased pass@k estimator (Chen et al., 2021): 1 - C(n-c, k) / C(n, k)."""
    if n <= 0 or k <= 0 or k > n:
        return None
    if n - c < k:
        return 1.0
    prod = 1.0
    for i in range(n - c + 1, n + 1):
        prod *= 1.0 - k / i
    return 1.0 - prod


def pareto_frontier(points: Sequence[tuple[str, float, float]]) -> list[str]:
    """Keys on the frontier minimizing x (cost/latency) and maximizing y (quality).

    O(n log n): sort by x asc (ties: y desc), sweep keeping strictly increasing best y.
    """
    ordered = sorted(points, key=lambda p: (p[1], -p[2]))
    frontier: list[str] = []
    best_y = -math.inf
    for key, _x, y in ordered:
        if y > best_y:
            frontier.append(key)
            best_y = y
    return frontier


def histogram(xs: Sequence[float], bins: int = 20) -> dict:
    s = [x for x in xs if x is not None]
    if not s:
        return {"edges": [], "counts": []}
    lo, hi = min(s), max(s)
    if hi == lo:
        return {"edges": [lo, hi], "counts": [len(s)]}
    width = (hi - lo) / bins
    counts = [0] * bins
    for x in s:
        idx = min(int((x - lo) / width), bins - 1)
        counts[idx] += 1
    return {"edges": [lo + i * width for i in range(bins + 1)], "counts": counts}
