"""Default stdlib HTTP/1.1 transport for the middleware runtime (experimental API).

A transport is any callable ``transport(method, url, headers, body, timeout) -> (status, headers, body)``
where ``timeout`` is the whole remaining budget in seconds, ``headers`` in the result is a mapping and
``body`` is bytes. The runtime refuses redirects, caps and parses bodies and classifies failures itself.

This one keeps connections alive, bounds DNS + connect + TLS + upload + download by that one budget,
honours ``HTTPS_PROXY`` / ``HTTP_PROXY`` / ``NO_PROXY`` (CONNECT tunnel for https, absolute-form for
http) and accepts an ``ssl.SSLContext`` for a custom CA or client certificate (mTLS).
"""
import base64
import http.client
import ipaddress
import socket
import ssl
import threading
import time
from collections.abc import Mapping
from urllib.parse import unquote, urlsplit

from .protocol import resolve_proxy

MAX_RESPONSE_BYTES = 4 << 20
_STALE = (http.client.RemoteDisconnected, BrokenPipeError, ConnectionResetError, ConnectionAbortedError)


def _left(deadline: float) -> float:
    left = deadline - time.monotonic()
    if left <= 0:
        raise TimeoutError("caveman middleware deadline")
    return left


def _resolve(host: str, port: int, deadline: float) -> list:
    """getaddrinfo ignores socket timeouts, so wait for it on a daemon thread instead."""
    try:
        ipaddress.ip_address(host)
        return socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)  # numeric: no lookup
    except ValueError:
        pass
    box: list = []
    done = threading.Event()

    def lookup():
        try:
            box.append(socket.getaddrinfo(host, port, type=socket.SOCK_STREAM))
        except BaseException as error:  # noqa: BLE001 - re-raised on the caller's thread
            box.append(error)
        done.set()

    # ponytail: one short-lived daemon thread per new connection's lookup; keep-alive makes lookups rare.
    threading.Thread(target=lookup, name="caveman-dns", daemon=True).start()
    if not done.wait(_left(deadline)):
        raise TimeoutError("caveman middleware DNS deadline")
    if isinstance(box[0], BaseException):
        raise box[0]
    return box[0]


def _dial(address: tuple, deadline: float) -> socket.socket:
    error: OSError | None = None
    for family, kind, proto, _, target in _resolve(address[0], address[1], deadline):
        sock = socket.socket(family, kind, proto)
        try:
            sock.settimeout(_left(deadline))
            sock.connect(target)
            return sock
        except OSError as failure:
            sock.close()
            error = failure
    raise error or OSError("no address")


class HTTPTransport:
    """Thread-safe keep-alive pool; one per runtime. See the module docstring for the contract."""

    def __init__(self, *, ssl_context: ssl.SSLContext | None = None, env: Mapping[str, str] | None = None, max_idle: int = 16):
        self._ssl, self._env, self._max_idle = ssl_context, env, max_idle
        self._reset()

    def _reset(self) -> None:
        """Also the post-fork hook: inherited sockets belong to the parent and are dropped, never closed."""
        self._lock = threading.Lock()
        self._idle: dict[tuple, list] = {}
        self._active: set = set()
        self._closed = False

    def __call__(self, method: str, url: str, headers: Mapping[str, str], body: bytes | None, timeout: float):
        deadline = time.monotonic() + timeout
        parts = urlsplit(url)
        proxy = resolve_proxy(url, self._env)
        key = (parts.scheme, parts.hostname, parts.port, proxy)
        target = parts.path or "/"
        headers = dict(headers)
        if proxy and parts.scheme == "http":
            target = url  # absolute-form through a forward proxy
            credentials = self._proxy_credentials(proxy)
            if credentials:
                headers["Proxy-Authorization"] = credentials
        for attempt in (0, 1):
            connection, reused = self._checkout(key, parts, proxy, deadline)
            try:
                return self._exchange(connection, key, method, target, headers, body, deadline)
            except _STALE:
                self._drop(connection)
                if not reused or attempt:  # only a reused idle connection may have been closed by the server
                    raise
            except BaseException:
                self._drop(connection)
                raise
        raise AssertionError("unreachable")

    def close(self) -> None:
        with self._lock:
            self._closed = True
            idle = [c for pool in self._idle.values() for c in pool]
            active = list(self._active)
            self._idle.clear()
            self._active.clear()
        for connection in active:
            try:  # shutdown wakes a thread blocked in recv; close alone does not on every platform
                connection.sock and connection.sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        for connection in idle + active:
            connection.close()

    @staticmethod
    def _proxy_credentials(proxy: str) -> str | None:
        p = urlsplit(proxy if "://" in proxy else "http://" + proxy)
        if p.username is None:
            return None
        raw = f"{unquote(p.username)}:{unquote(p.password or '')}".encode()
        return "Basic " + base64.b64encode(raw).decode("ascii")

    def _connect(self, parts, proxy: str | None, deadline: float) -> http.client.HTTPConnection:
        host, port = parts.hostname, parts.port or (443 if parts.scheme == "https" else 80)
        via_host, via_port = host, port
        if proxy:
            p = urlsplit(proxy if "://" in proxy else "http://" + proxy)
            if p.scheme != "http" or not p.hostname:
                # ponytail: TLS to the proxy itself (https:// proxy URLs) is unsupported; add when a user needs it.
                raise OSError("unsupported proxy URL")
            via_host, via_port = p.hostname, p.port or 80
        if parts.scheme == "https":
            connection = http.client.HTTPSConnection(via_host, via_port, context=self._ssl or ssl.create_default_context())
            if proxy:
                credentials = self._proxy_credentials(proxy)
                connection.set_tunnel(host, port, headers={"Proxy-Authorization": credentials} if credentials else None)
        else:
            connection = http.client.HTTPConnection(via_host, via_port)
        connection.timeout = _left(deadline)
        connection._create_connection = lambda address, *_: _dial(address, deadline)
        connection.connect()  # dial + CONNECT tunnel + TLS handshake, each bounded by the socket timeout
        return connection

    def _checkout(self, key, parts, proxy, deadline):
        with self._lock:
            if self._closed:
                raise OSError("transport closed")
            pool = self._idle.get(key)
            connection = pool.pop() if pool else None
            if connection is not None:
                self._active.add(connection)
                return connection, True
        connection = self._connect(parts, proxy, deadline)
        with self._lock:
            if not self._closed:
                self._active.add(connection)
                return connection, False
        connection.close()
        raise OSError("transport closed")

    def _drop(self, connection) -> None:
        with self._lock:
            self._active.discard(connection)
        connection.close()

    def _exchange(self, connection, key, method, target, headers, body, deadline):
        connection.sock.settimeout(_left(deadline))
        connection.request(method, target, body=body, headers=headers)
        connection.sock.settimeout(_left(deadline))
        response = connection.getresponse()
        data = bytearray()
        while len(data) <= MAX_RESPONSE_BYTES:
            connection.sock.settimeout(_left(deadline))
            part = response.read1(min(65536, MAX_RESPONSE_BYTES + 1 - len(data)))
            if not part:
                break
            data += part
        result = response.status, {k.lower(): v for k, v in response.getheaders()}, bytes(data)
        with self._lock:
            self._active.discard(connection)
            pool = self._idle.setdefault(key, [])
            keep = response.isclosed() and not response.will_close and not self._closed and len(pool) < self._max_idle
            if keep:
                pool.append(connection)
        if not keep:
            connection.close()
        return result
