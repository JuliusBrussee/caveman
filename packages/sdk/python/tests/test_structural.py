"""Tests for the W15 structural SDK surface: retry-loop breaker + async jobs.

Mirrors the TypeScript SDK's structural.runtime.mjs tests (same shape/semantics).
"""

from __future__ import annotations

from unittest.mock import patch

import pytest

from caveman_cloud import AsyncJobsUnavailableError, Cave, RetryLoopBreaker, RetryLoopError


# --- Retry-loop breaker ----------------------------------------------------


def test_breaker_interrupts_after_threshold() -> None:
    breaker = RetryLoopBreaker(threshold=3)  # fires on the 4th identical call
    args = {"id": 42}
    for _ in range(3):
        breaker.record("fetch_order", args)  # tolerated
    with pytest.raises(RetryLoopError) as exc:
        breaker.record("fetch_order", args)
    assert exc.value.repeats == 4
    assert exc.value.threshold == 3


def test_cave_retry_loop_breaker_default_fires_once() -> None:
    cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
    breaker = cave.retry_loop_breaker()
    fired = 0
    for _ in range(10):
        try:
            breaker.record("loop_tool", {"same": True})
        except RetryLoopError:
            fired += 1
            break
    assert fired == 1


def test_breaker_different_calls_reset_streak() -> None:
    breaker = RetryLoopBreaker(threshold=2)
    breaker.record("a", {"x": 1})
    breaker.record("a", {"x": 1})
    # Different args reset the streak — no interrupt.
    breaker.record("a", {"x": 2})
    breaker.record("a", {"x": 2})
    # Key ORDER does not matter (sorted-key signature).
    breaker.reset()
    breaker.record("a", {"x": 1, "y": 2})
    breaker.record("a", {"y": 2, "x": 1})
    with pytest.raises(RetryLoopError):
        breaker.record("a", {"x": 1, "y": 2})


def test_breaker_guard_does_not_run_fn_on_interrupt() -> None:
    breaker = RetryLoopBreaker(threshold=1)
    calls = 0

    def fn() -> str:
        nonlocal calls
        calls += 1
        return "ok"

    assert breaker.guard("t", {"a": 1}, fn) == "ok"
    with pytest.raises(RetryLoopError):
        breaker.guard("t", {"a": 1}, fn)
    assert calls == 1  # fn must not run on the interrupted call


# --- Async job client ------------------------------------------------------


def test_all_job_methods_fail_locally_without_network_or_persistence_claims() -> None:
    cave = Cave(api_key="k", base_url="http://localhost:8787", agent="a")
    calls = [
        lambda: cave.jobs.submit({"kind": "summarize"}, latency_class="offline"),
        lambda: cave.jobs.status("job-9"),
        lambda: cave.jobs.wait("job-9"),
        lambda: cave.jobs.submit_and_wait({"kind": "audit"}),
        lambda: cave.jobs.cancel("job-9"),
    ]
    with patch("urllib.request.urlopen") as urlopen:
        for call in calls:
            with pytest.raises(AsyncJobsUnavailableError) as exc:
                call()
            assert exc.value.code == "cave_async_jobs_unavailable"
    urlopen.assert_not_called()
