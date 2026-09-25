"""Authenticated transport contract, using only distinct local HTTP origins."""

import json
import threading
import urllib.error
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from caveman_cloud import Cave

CONTRACT = json.loads((Path(__file__).resolve().parents[2] / "parity" / "fixtures.json").read_text())["transport"]


@contextmanager
def redirect_fixture(status=302, origin="cross"):
    requests = []
    redirected = []

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            self.handle_request()

        def do_GET(self):
            self.handle_request()

        def handle_request(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            entry = {"path": self.path, "body": body, "headers": dict(self.headers), "method": self.command}
            if self.server is target or self.path == "/redirected":
                redirected.append(entry)
                data = json.dumps(CONTRACT["provider_response"]).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            else:
                requests.append(entry)
                self.send_response(status)
                redirect_port = source.server_port if origin == "same" else target.server_port
                self.send_header("Location", f"http://127.0.0.1:{redirect_port}/redirected")
                self.send_header("Content-Length", "0")
                self.end_headers()

        def log_message(self, *_args):
            pass

    target = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    source = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threads = [threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True) for server in (source, target)]
    for thread in threads:
        thread.start()
    cave = Cave(api_key="cave-fixture-key", base_url=f"http://127.0.0.1:{source.server_port}", agent="transport-test")
    try:
        yield cave, requests, redirected
    finally:
        for server in (source, target):
            server.shutdown()
            server.server_close()
        for thread in threads:
            thread.join(timeout=1)


@pytest.mark.parametrize("status", CONTRACT["redirect_statuses"])
@pytest.mark.parametrize("origin", CONTRACT["redirect_origins"])
def test_provider_credentials_never_follow_redirects(status, origin, monkeypatch):
    monkeypatch.setenv("no_proxy", "127.0.0.1")
    with redirect_fixture(status, origin) as (cave, requests, redirected):
        with pytest.raises(urllib.error.HTTPError, match="cave_redirect_not_allowed"):
            cave.openai(upstream_key="upstream-fixture-key").responses.create(CONTRACT["provider_body"])
        assert len(requests) == 1
        assert json.loads(requests[0]["body"]) == CONTRACT["provider_body"]
        request_headers = {key.lower(): value for key, value in requests[0]["headers"].items()}
        assert request_headers["authorization"] == "Bearer cave-fixture-key"
        assert request_headers["x-cave-upstream-key"] == "upstream-fixture-key"
        assert redirected == []


def test_raw_otlp_and_compression_share_the_redirect_guard(monkeypatch):
    monkeypatch.setenv("no_proxy", "127.0.0.1")
    with redirect_fixture() as (cave, requests, redirected):
        with pytest.raises(urllib.error.HTTPError, match="cave_redirect_not_allowed"):
            cave.openai(upstream_key="upstream-fixture-key").raw("/responses", CONTRACT["provider_body"])
        exporter = cave.exporter()
        exporter.record_span("fixture")
        with pytest.raises(urllib.error.HTTPError, match="cave_redirect_not_allowed"):
            exporter.export()
        assert exporter.pending == 1
        assert requests[1]["headers"].get("X-Cave-Api-Key", requests[1]["headers"].get("X-cave-api-key")) == "cave-fixture-key"
        result = cave.compress("original bytes")
        assert result.output == "original bytes"
        assert result.ratio == 0
        assert result.recovery_handle is None
        assert len(requests) == 3
        assert redirected == []
