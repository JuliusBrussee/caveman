"""Default middleware transport against real local sockets: keep-alive, deadlines, proxies, TLS (B9)."""
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

from caveman_cloud.middleware import MiddlewareRuntime

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
    server.connections, server.requests, server.upstream = 0, [], upstream
    if tls is not None:
        server.socket = tls.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


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


if __name__ == "__main__":
    unittest.main()
