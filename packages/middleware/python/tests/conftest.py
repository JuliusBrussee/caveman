"""Shared fail-open fixtures: a runtime whose proxy is guaranteed unreachable."""
import socket

import pytest


def _closed_port():
    """A port nothing is listening on. Connecting to it refuses immediately."""
    probe = socket.socket()
    probe.bind(("127.0.0.1", 0))
    port = probe.getsockname()[1]
    probe.close()
    return port


@pytest.fixture
def unreachable_runtime():
    """The failure every adapter must survive: the local proxy is not running."""
    MiddlewareRuntime = pytest.importorskip(
        "caveman_cloud.middleware", reason="caveman-sdk is not installed").MiddlewareRuntime
    reports = []
    runtime = MiddlewareRuntime(endpoint=f"http://127.0.0.1:{_closed_port()}", deadline_ms=200,
                                on_report=reports.append)
    runtime.reports = reports
    try:
        yield runtime
    finally:
        runtime.close()


def requires(module):
    """Skip when the adapter's framework is not installed in this environment."""
    return pytest.importorskip(module, reason=f"{module} is not installed; adapter tests need the real framework")


def bypassed(runtime):
    """Reports where the adapter chose the caller's own input, not a plan.

    An unreachable runtime can only ever produce these: `skipped` when the
    adapter asked and got nothing back, `disabled` when it never asked.
    """
    return [event for event in runtime.reports if event.status in ("skipped", "disabled")]
