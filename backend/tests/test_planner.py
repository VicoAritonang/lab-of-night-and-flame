import pytest

from arl.engine.planner import expand_arms, schedule
from arl.engine.ratelimit import TokenBucket
from arl.models import ExperimentPlan


def _plan(**kw):
    base = {"dataset_id": "ds",
            "targets": [{"provider": "fake", "model": "fake/oracle-90"}, {"provider": "fake", "model": "fake/oracle-50"}],
            "scorers": [{"id": "em", "key": "exact_match"}], "primary_scorer": "em"}
    base.update(kw)
    return ExperimentPlan.model_validate(base)


def test_arm_expansion_cartesian_and_stable_keys():
    p = _plan(params_grid={"temperature": [0, 0.7]},
              prompts=[{"id": "a"}, {"id": "b", "system": "Be brief. {instructions}"}])
    arms = expand_arms(p)
    assert len(arms) == 2 * 2 * 2
    assert [a.key for a in arms] == [a.key for a in expand_arms(p)]
    assert len({a.key for a in arms}) == 8


def test_schedule_is_seeded_and_complete():
    a = schedule(["x", "y"], ["s1", "s2", "s3"], 2, "interleaved", 1)
    b = schedule(["x", "y"], ["s1", "s2", "s3"], 2, "interleaved", 1)
    c = schedule(["x", "y"], ["s1", "s2", "s3"], 2, "interleaved", 2)
    assert a == b and a != c and len(a) == 12 and len(set(a)) == 12
    blocked = schedule(["x", "y"], ["s1", "s2"], 1, "blocked", 3)
    assert blocked[0][1] == blocked[1][1]  # both arms of a sample run back-to-back


def test_plan_validation_rejects_bad_primary_and_grid():
    with pytest.raises(Exception):
        _plan(primary_scorer="missing")
    with pytest.raises(Exception):
        _plan(params_grid={"model": ["x"]})


async def test_token_bucket_rate():
    clock = [0.0]
    b = TokenBucket(60, burst=2, clock=lambda: clock[0])
    assert await b.acquire() == 0 and await b.acquire() == 0
    clock[0] += 1.0  # one token per second at 60 rpm
    assert await b.acquire() == 0
