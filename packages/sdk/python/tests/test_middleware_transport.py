"""Default middleware transport against real local sockets: keep-alive, deadlines, proxies, TLS (B9)."""
import asyncio
import hashlib
import json
import os
import select
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

from caveman_cloud.middleware import Adapter, Candidate, MiddlewareRuntime

BASE = json.loads((Path(__file__).resolve().parents[2] / "parity" / "middleware.fixtures.json").read_text(encoding="utf-8"))
CAPS = json.dumps(BASE["capabilities"]).encode()
NO_PROXY_ENV = {"http_proxy": "", "HTTP_PROXY": "", "https_proxy": "", "HTTPS_PROXY": "", "no_proxy": "", "NO_PROXY": ""}


class Runtime(BaseHTTPRequestHandler):
    """A capabilities-only runtime that counts TCP connections."""
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        self.server.connections += 1

    def do_GET(self):
        self.server.requests.append((self.path, dict(self.headers)))
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(CAPS)))
        self.end_headers()
        self.wfile.write(CAPS)

    def log_message(self, *_):
        pass


class Proxy(Runtime):
    """CONNECT tunnels to the upstream port; absolute-form GETs are answered directly."""

    def do_CONNECT(self):
        self.server.requests.append((self.requestline, dict(self.headers)))
        upstream = socket.create_connection(("127.0.0.1", self.server.upstream))
        self.send_response(200, "Connection established")
        self.end_headers()
        pair = [self.connection, upstream]
        while True:
            readable, _, _ = select.select(pair, [], [], 5)
            if not readable:
                break
            source = readable[0]
            data = source.recv(65536)
            if not data:
                break
            (upstream if source is self.connection else self.connection).sendall(data)
        upstream.close()
        self.close_connection = True

    def do_GET(self):
        self.server.requests.append((self.requestline, dict(self.headers)))
        super().do_GET()


def serve(handler, tls: ssl.SSLContext | None = None, upstream: int | None = None):
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    server.daemon_threads = True
    server.socket.listen(64)  # the default backlog of 5 resets a burst of concurrent connects
    server.connections, server.requests, server.upstream = 0, [], upstream
    if tls is not None:
        server.socket = tls.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def raw_server(handler):
    """A raw socket server: handler(conn) runs per accepted connection on its own thread."""
    server = socket.create_server(("127.0.0.1", 0))

    def accept():
        while True:
            try:
                conn, _ = server.accept()
            except OSError:
                return
            threading.Thread(target=handler, args=(conn,), daemon=True).start()
    threading.Thread(target=accept, daemon=True).start()
    return server


def drip(head: bytes, data: bytes, every: float = 0.05):
    """Answer one request with `head`, then `data` one byte per `every` seconds: each read beats a per-read timeout."""
    def handler(conn):
        try:
            conn.recv(65536)
            conn.sendall(head)
            for i in range(len(data)):
                conn.sendall(data[i:i + 1])
                time.sleep(every)
        except OSError:
            pass
        finally:
            conn.close()
    return handler


class Stale(Runtime):
    """A runtime that idle-closes keep-alive connections after 0.5 s and answers optimize with a bypass plan."""
    timeout = 0.5

    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        request = json.loads(body)
        if self.server.barrier is not None:  # hold concurrent calls together, so each opens its own pooled connection
            self.server.barrier.wait(5)
        plan = json.dumps({**BASE["plan"], "request_id": request["request_id"], "input_digest": hashlib.sha256(body).hexdigest(),
                           "status": "bypassed", "reason": "not_smaller", "replacements": [],
                           "skipped": [{"segment_id": s["id"], "reason": "not_smaller"} for s in request["segments"]],
                           "measurement": {**BASE["plan"]["measurement"], "tokens_after": 1000, "unique_tokens_reduced": 0}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(plan)))
        self.end_headers()
        self.wfile.write(plan)


class TestDefaultTransport(unittest.TestCase):
    def tearDown(self):
        for server in getattr(self, "servers", []):
            server.shutdown()
            server.server_close()

    def start(self, *args, **kwargs):
        server = serve(*args, **kwargs)
        self.servers = [*getattr(self, "servers", []), server]
        return server

    def test_keep_alive_reuses_one_connection_under_a_path_prefix(self):
        # B9: 12 requests used to open 13 TCP connections.
        server = self.start(Runtime)
        with patch.dict(os.environ, NO_PROXY_ENV), MiddlewareRuntime(endpoint=f"http://127.0.0.1:{server.server_port}/rt/", deadline_ms=5000) as runtime:
            for _ in range(12):
                runtime.ready()
        self.assertEqual(server.connections, 1)
        self.assertEqual({path for path, _ in server.requests}, {"/rt/caveman/v1/middleware/capabilities"})
        headers = server.requests[0][1]
        self.assertEqual(headers["Caveman-Middleware-Features"], "http_status_v2, revision_tolerant")
        self.assertTrue(headers["Caveman-Middleware-Client"].startswith("caveman-sdk-python/"))

    def test_dns_resolution_counts_inside_the_deadline(self):
        # B9: a 100 ms deadline took 2 s when name resolution hung.
        real = socket.getaddrinfo

        def slow(host, *args, **kwargs):
            if host == "slow.invalid":
                time.sleep(6)
            return real(host, *args, **kwargs)

        with patch.dict(os.environ, NO_PROXY_ENV), patch("socket.getaddrinfo", slow), \
                MiddlewareRuntime(endpoint="https://slow.invalid", allow_remote_content=True, deadline_ms=100) as runtime:
            started = time.monotonic()
            self.assertEqual(runtime.preflight().reason, "deadline")
            self.assertLess(time.monotonic() - started, 3)

    def test_http_proxy_forwards_absolute_form(self):
        proxy = self.start(Proxy)
        env = {**NO_PROXY_ENV, "HTTP_PROXY": f"http://user:p%40ss@127.0.0.1:{proxy.server_port}"}
        with patch.dict(os.environ, env), MiddlewareRuntime(endpoint="http://runtime.test:8787", allow_remote_content=True,
                                                              allow_insecure_transport=True, deadline_ms=5000) as runtime:
            self.assertEqual(runtime.ready()["runtime_build"], "protocol-fixture")
        line, headers = proxy.requests[0]
        self.assertEqual(line, "GET http://runtime.test:8787/caveman/v1/middleware/capabilities HTTP/1.1")
        self.assertEqual(headers["Proxy-Authorization"], "Basic dXNlcjpwQHNz")

    def test_no_proxy_and_loopback_bypass_the_proxy(self):
        proxy, server = self.start(Proxy), self.start(Runtime)
        env = {**NO_PROXY_ENV, "HTTP_PROXY": f"http://127.0.0.1:{proxy.server_port}"}
        with patch.dict(os.environ, env), MiddlewareRuntime(endpoint=f"http://127.0.0.1:{server.server_port}", deadline_ms=5000) as runtime:
            runtime.ready()
        self.assertEqual((proxy.requests, len(server.requests)), ([], 1))

    @unittest.skipUnless(shutil.which("openssl"), "openssl CLI mints the throwaway certificate")
    def test_https_proxy_connect_tunnel_with_custom_ca(self):
        with tempfile.TemporaryDirectory() as tmp:
            cert, key = Path(tmp, "cert.pem"), Path(tmp, "key.pem")
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(cert),
                            "-days", "1", "-subj", "/CN=runtime.test", "-addext", "subjectAltName=DNS:runtime.test"],
                           check=True, capture_output=True)
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(cert, key)
            upstream = self.start(Runtime, tls=tls)
            proxy = self.start(Proxy, upstream=upstream.server_port)
            env = {**NO_PROXY_ENV, "HTTPS_PROXY": f"http://127.0.0.1:{proxy.server_port}"}
            client = ssl.create_default_context(cafile=str(cert))
            with patch.dict(os.environ, env), MiddlewareRuntime(endpoint=f"https://runtime.test:{upstream.server_port}",
                                                                  allow_remote_content=True, ssl_context=client, deadline_ms=5000) as runtime:
                for _ in range(3):
                    self.assertEqual(runtime.ready()["runtime_build"], "protocol-fixture")
            # Python 3.11 sends CONNECT as HTTP/1.0, 3.12+ as HTTP/1.1; the target is what matters.
            self.assertEqual([line.rsplit(" ", 1)[0] for line, _ in proxy.requests], [f"CONNECT runtime.test:{upstream.server_port}"])
            self.assertEqual(len(upstream.requests), 3)
            with patch.dict(os.environ, env), MiddlewareRuntime(endpoint=f"https://runtime.test:{upstream.server_port}",
                                                                  allow_remote_content=True, deadline_ms=5000) as untrusted:
                self.assertEqual(untrusted.preflight().reason, "runtime_unavailable", "the system trust store rejects the test CA")


    def assert_bounded(self, runtime, deadline_s):
        started = time.monotonic()
        reason = runtime.preflight().reason
        elapsed = time.monotonic() - started
        self.assertEqual(reason, "deadline")
        self.assertLess(elapsed, deadline_s + 0.1, "the whole exchange is bounded, not each read")

    def test_trickled_response_phases_are_bounded_by_the_deadline(self):
        # A peer trickling one byte per 150 ms held a 500 ms call for ~63 s: socket timeouts bound single reads only.
        status = b"HTTP/1.1 200 OK\r\n"
        phases = {"status line": drip(b"", status + b"Content-Length: 2\r\n\r\n{}"),
                  "headers": drip(status, b"X-Slow: " + b"a" * 40 + b"\r\n"),
                  "chunked body": drip(status + b"Transfer-Encoding: chunked\r\n\r\n", b"1" * 40 + b"\r\n"),
                  "content-length body": drip(status + b"Content-Length: 40\r\n\r\n", b"{" + b" " * 39)}
        for phase, handler in phases.items():
            with self.subTest(phase):
                server = raw_server(handler)
                self.addCleanup(server.close)
                with patch.dict(os.environ, NO_PROXY_ENV), MiddlewareRuntime(endpoint=f"http://127.0.0.1:{server.getsockname()[1]}",
                                                                              deadline_ms=300) as runtime:
                    self.assert_bounded(runtime, 0.3)

    def test_trickled_connect_tunnel_is_bounded_by_the_deadline(self):
        proxy = raw_server(drip(b"", b"HTTP/1.1 200 Connection established\r\nX-Slow: " + b"a" * 40 + b"\r\n\r\n"))
        self.addCleanup(proxy.close)
        env = {**NO_PROXY_ENV, "HTTPS_PROXY": f"http://127.0.0.1:{proxy.getsockname()[1]}"}
        with patch.dict(os.environ, env), MiddlewareRuntime(endpoint="https://runtime.test", allow_remote_content=True, deadline_ms=300) as runtime:
            self.assert_bounded(runtime, 0.3)

    @unittest.skipUnless(shutil.which("openssl"), "openssl CLI mints the throwaway certificate")
    def test_trickled_tls_handshake_is_bounded_by_the_deadline(self):
        with tempfile.TemporaryDirectory() as tmp:
            cert, key = Path(tmp, "cert.pem"), Path(tmp, "key.pem")
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(cert),
                            "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1"], check=True, capture_output=True)
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(cert, key)
            upstream = self.start(Runtime, tls=tls)

            def relay(conn):  # forwards the real handshake, the server's bytes one per 50 ms
                far = socket.create_connection(("127.0.0.1", upstream.server_port))
                try:
                    ends = time.monotonic() + 3
                    while time.monotonic() < ends:
                        readable, _, _ = select.select([conn, far], [], [], 0.05)
                        if conn in readable:
                            far.sendall(conn.recv(65536) or b"")
                        if far in readable:
                            data = far.recv(1)
                            if not data:
                                break
                            conn.sendall(data)
                            time.sleep(0.05)
                except OSError:
                    pass
                finally:
                    far.close()
                    conn.close()
            server = raw_server(relay)
            self.addCleanup(server.close)
            with patch.dict(os.environ, NO_PROXY_ENV), MiddlewareRuntime(endpoint=f"https://127.0.0.1:{server.getsockname()[1]}", deadline_ms=300,
                                                                          ssl_context=ssl.create_default_context(cafile=str(cert))) as runtime:
                self.assert_bounded(runtime, 0.3)

    def test_idle_closed_pooled_connections_are_never_reused(self):
        # The runtime and load balancers idle-close keep-alive connections; 12 dead pooled sockets used to fail 5 calls
        # in a row and open the breaker against a healthy runtime.
        server = self.start(Stale)
        server.barrier = threading.Barrier(12)
        adapter, scope = Adapter("t", "1", "1", "1"), {"namespace": "n", "session_id": "s"}
        with patch.dict(os.environ, NO_PROXY_ENV), MiddlewareRuntime(endpoint=f"http://127.0.0.1:{server.server_port}", deadline_ms=5000) as runtime:
            binding = runtime.recovery(scope)

            def call():
                return runtime.optimize(scope=scope, adapter=adapter, candidates=[Candidate("c1", "x" * 4000)], manifest=[], binding=binding).reason
            runtime.ready()
            barrier = threading.Barrier(12)
            threads = [threading.Thread(target=lambda: (barrier.wait(), call())) for _ in range(12)]
            [t.start() for t in threads]
            [t.join() for t in threads]
            server.barrier = None
            self.assertEqual(sum(map(len, runtime._transport._idle.values())), 12)
            time.sleep(1)  # the server idle-closes every pooled connection
            self.assertEqual([call() for _ in range(8)], ["not_smaller"] * 8)
            self.assertEqual(runtime._breaker.state, "closed")


class TestAsyncView(unittest.IsolatedAsyncioTestCase):
    async def test_view_aclose_never_waits_on_a_stuck_worker(self):
        # aclose() of an as_async() view joined its workers, so one stuck custom transport held it indefinitely.
        stuck = threading.Event()
        self.addCleanup(stuck.set)
        caps = json.loads(CAPS)

        def transport(method, url, headers, body, timeout):
            if url.endswith("/capabilities"):
                return 200, {}, json.dumps(caps).encode()
            stuck.wait(10)
            raise OSError("released")
        runtime = MiddlewareRuntime(transport=transport, deadline_ms=200)
        self.addCleanup(runtime.close)
        view, scope = runtime.as_async(), {"namespace": "n", "session_id": "s"}
        result = await view.optimize(scope=scope, adapter=Adapter("t", "1", "1", "1"), candidates=[Candidate("c1", "x" * 400)],
                                     manifest=[], binding=runtime.recovery(scope))
        self.assertEqual(result.reason, "deadline")
        started = time.monotonic()
        await view.aclose()
        self.assertLess(time.monotonic() - started, 0.5)


if __name__ == "__main__":
    unittest.main()
