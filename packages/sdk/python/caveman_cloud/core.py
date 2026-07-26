from __future__ import annotations

import json
import math
import os
import secrets
import time
import urllib.request
from contextlib import contextmanager
from urllib.parse import quote
from dataclasses import dataclass, field
from typing import Any, Callable, Iterator


def _strict_non_negative_int(value: Any) -> int | None:
    """Accept JSON integers only; reject bools, floats, strings, and negatives."""
    if isinstance(value, bool) or not isinstance(value, int) or value < 0 or value > 2**53 - 1:
        return None
    return value


class RetryLoopError(RuntimeError):
    """Raised by :class:`RetryLoopBreaker` when an identical tool-call repeats
    more times than the configured threshold.

    The breaker is a structural safety net: agents that get stuck re-issuing the
    same tool call (same name + same arguments) burn tokens without progress.
    Rather than letting the loop run, the SDK interrupts it deterministically.
    """

    def __init__(self, signature: str, repeats: int, threshold: int) -> None:
        self.signature = signature
        self.repeats = repeats
        self.threshold = threshold
        super().__init__(
            f"retry loop interrupted: tool call {signature!r} repeated "
            f"{repeats} times (threshold {threshold})"
        )


@dataclass
class RetryLoopBreaker:
    """Detects and interrupts a repeated identical tool-call loop.

    Call :meth:`record` (or :meth:`guard`) before each tool invocation with the
    tool name and arguments. When the SAME (name, arguments) signature repeats
    consecutively more than ``threshold`` times, :meth:`record` raises
    :class:`RetryLoopError`. Any different call resets the streak.

    Mirrors the TypeScript ``RetryLoopBreaker``: same field names, same
    threshold semantics (the breaker fires on the call that would be the
    ``threshold + 1``-th consecutive identical call).
    """

    threshold: int = 3
    _last_signature: str | None = field(default=None, init=False, repr=False)
    _repeats: int = field(default=0, init=False, repr=False)

    def signature(self, name: str, arguments: Any) -> str:
        """Canonical signature for a tool call (name + sorted-key JSON args)."""
        try:
            args = json.dumps(arguments, sort_keys=True, separators=(",", ":"))
        except (TypeError, ValueError):
            args = repr(arguments)
        return f"{name}({args})"

    def record(self, name: str, arguments: Any) -> None:
        """Record a tool call. Raises :class:`RetryLoopError` once an identical
        call has repeated past the threshold. A different call resets the streak.
        """
        sig = self.signature(name, arguments)
        if sig == self._last_signature:
            self._repeats += 1
        else:
            self._last_signature = sig
            self._repeats = 1
        if self._repeats > self.threshold:
            raise RetryLoopError(sig, self._repeats, self.threshold)

    def guard(self, name: str, arguments: Any, fn: Callable[[], Any]) -> Any:
        """Record the call (may raise) then invoke ``fn``."""
        self.record(name, arguments)
        return fn()

    def reset(self) -> None:
        """Clear the streak (e.g. when starting a new task)."""
        self._last_signature = None
        self._repeats = 0


@dataclass
class Job:
    """Reserved result shape for future durable async-job execution."""

    id: str
    state: str
    raw: dict[str, Any] = field(default_factory=dict)

    @property
    def done(self) -> bool:
        return self.state in ("completed", "failed", "cancelled")

    @property
    def ok(self) -> bool:
        return self.state == "completed"


@dataclass
class ToolSearchResult:
    """Result from a server-side tool-search call."""
    tools: list[dict[str, Any]]
    sent_schema_tokens: int
    full_schema_tokens: int
    deferred_count: int
    method: str
    session_id: str | None = None
    token_basis: str = "unavailable"
    basis: str = "inferred"

    @property
    def saved_tokens(self) -> int:
        if self.full_schema_tokens < 0 or self.sent_schema_tokens < 0:
            return 0
        return max(0, self.full_schema_tokens - self.sent_schema_tokens)

    @property
    def reduction_pct(self) -> float:
        if self.full_schema_tokens <= 0 or self.sent_schema_tokens > self.full_schema_tokens:
            return 0.0
        # Match Go math.Round / JavaScript Math.round for non-negative values;
        # Python's built-in round uses bankers rounding on exact ties.
        value = 100.0 * self.saved_tokens / self.full_schema_tokens
        return math.floor(value * 10.0 + 0.5) / 10.0


@dataclass
class CompressResult:
    """Result of a :meth:`Cave.compress` call — the Engine's compression report.

    ``basis`` is always ``"inferred"``: the SDK never emits ``verified`` (that is
    earned only by the Cloud ``active`` path). On any transport/parse problem the
    call is a fail-closed pass-through (``output`` is the original input,
    ``ratio`` is ``0.0``, ``recovery_handle`` is ``None``).

    Mirrors the TypeScript ``CompressResult`` (same field names).
    """

    output: str
    content_type: str
    tokens_before: int
    tokens_after: int
    ratio: float
    basis: str = "inferred"
    recovery_handle: str | None = None
    method: str | None = None
    lossless_to_model: bool | None = None
    token_count_basis: str = "unavailable"


@dataclass
class CaveTool:
    """A tool descriptor for inclusion in the tool catalog."""
    name: str
    description: str
    input_schema: dict[str, Any]
    read_only: bool = False
    always_load: bool = False


@dataclass
class TaskProfile:
    """The single human-editable object controlling cave-auto routing for a
    workflow (spec R14). Field names are the snake_case wire names — identical to
    the Go ``policy.TaskProfile`` JSON tags and the TypeScript ``TaskProfile``
    interface. Editing one is a policy publish; there is no ML in the loop.

    ``alpha`` is the 0–10 cost/quality dial (0 = most capable in the passing set,
    10 = cheapest). Every field is optional; an absent profile means baseline
    pass-through.
    """

    quality_floor: float = 0.0
    alpha: float = 0.0
    candidate_allowlist: list[str] = field(default_factory=list)
    candidate_denylist: list[str] = field(default_factory=list)
    max_p95_latency_delta_ms: int = 0
    max_error_delta: float = 0.0
    max_cost_ratio: float = 0.0
    cascade_enabled: bool = False
    cascade_tau: float = 0.0
    max_escalation_rate: float = 0.0
    stickiness: str = ""
    cross_provider: bool = False
    data_residency: list[str] = field(default_factory=list)
    trusted_route_hints: list[str] = field(default_factory=list)


_WORKFLOW_CHARS = set("abcdefghijklmnopqrstuvwxyz0123456789_-")


def _env_workflow() -> str:
    """CAVE_WORKFLOW normalized to the gateway label rule, else the honest default."""
    raw = (os.environ.get("CAVE_WORKFLOW") or "").lower()
    if raw and len(raw) <= 96 and all(c in _WORKFLOW_CHARS for c in raw):
        return raw
    return "unlabeled-workflow"


@dataclass
class Cave:
    api_key: str
    base_url: str
    agent: str
    # CAVE_WORKFLOW lets a wrapper (`cave wrap --workflow x`) label every request
    # from an SDK app without a code change. An explicit value always wins. The
    # env value is normalized to the gateway's label rule (lowercase [a-z0-9_-],
    # max 96); an invalid ambient value is ignored rather than 400-ing every
    # request. Mirrors @caveman/sdk (TypeScript).
    default_workflow: str = field(default_factory=lambda: _env_workflow())
    retention: str = "metadata"
    verify_on_init: bool = False
    # control_url is reserved for control-plane APIs and defaults to base_url.
    control_url: str | None = None
    # Opaque end-user identifier (never a Caveman member id); sent as
    # x-cave-user-hash so spend can be grouped by the caller's own users.
    user: str | None = None

    @contextmanager
    def trace(self, workflow: str | None = None, tags: dict[str, str] | None = None) -> Iterator["Trace"]:
        yield Trace(self, workflow or self.default_workflow, tags or {})

    @property
    def jobs(self) -> "JobsClient":
        """Reserved async-job surface; methods fail locally without network I/O."""
        return JobsClient(self)

    @property
    def shared_context(self) -> "_SharedContext":
        """Session-keyed multi-agent shared context. Mirrors the TS ``cave.sharedContext``."""
        return _SharedContext(self)

    @property
    def prompts(self) -> "_Prompts":
        """Prompt-snippet helpers. Mirrors the TS ``cave.prompts`` namespace."""
        return _Prompts()

    def retry_loop_breaker(self, threshold: int = 3) -> RetryLoopBreaker:
        """A fresh :class:`RetryLoopBreaker` that interrupts a repeated identical
        tool-call loop after ``threshold`` consecutive repeats.
        """
        return RetryLoopBreaker(threshold=threshold)

    def openai(self, upstream_key: str | None = None) -> "Provider":
        return Provider(self, "/openai/v1", upstream_key)

    def anthropic(self, upstream_key: str | None = None) -> "Provider":
        return Provider(self, "/anthropic", upstream_key)

    def gemini(self, upstream_key: str | None = None) -> "Provider":
        return Provider(self, "/gemini", upstream_key)

    def bedrock(self, region: str, *, endpoint: str = "runtime") -> dict[str, Any]:
        """Describe the first-party Bedrock gateway surface.

        Runtime is the default. Mantle is an explicit opt-in descriptor. This
        method performs no network request and carries no AWS secret.
        """
        if endpoint not in {"runtime", "mantle"}:
            raise ValueError("bedrock endpoint must be runtime or mantle")
        return {
            "region": region,
            "endpoint": endpoint,
            "gateway_prefix": "/bedrock/anthropic" if endpoint == "mantle" else "/bedrock",
            "instrumented": True,
            "sdk_only": False,
        }

    def vertex(self, upstream_key: str | None = None) -> "Provider":
        """Vertex AI client proxied through the gateway (``/vertex`` prefix).

        Routes Google Gemini and Anthropic Claude calls through Caveman for
        metering. ``upstream_key`` is a Google OAuth2 access token (e.g. from
        ``gcloud auth print-access-token`` or Application Default Credentials);
        the gateway forwards it as ``Authorization: Bearer …``. Mirrors the
        TypeScript ``Cave.vertex``.
        """
        return Provider(self, "/vertex", upstream_key)

    def exporter(self, *, service_name: str | None = None) -> "OTelExporter":
        """Create a one-call OTel exporter bound to this Cave's gateway config.

        The exporter ships spans to the gateway's OTLP endpoint
        (``POST {base_url}/otlp/v1/traces``) with the Caveman headers
        (``x-cave-api-key`` / ``x-cave-agent`` / ``x-cave-workflow``) so that
        ``record_span()`` + ``export()`` land rows in ``caveman.spans`` without
        any external OpenTelemetry wiring.

        Args:
            service_name: ``service.name`` resource attribute; defaults to the
                Cave agent slug (the gateway falls back to it for the agent label).

        Returns:
            An :class:`OTelExporter` whose spans inherit this Cave's
            ``api_key`` / ``base_url`` / ``agent`` / ``default_workflow``.
        """
        return OTelExporter(self, service_name=service_name or self.agent)

    def tools(
        self,
        catalog: list[CaveTool],
        *,
        strategy: str = "all",
        initial_tool_count: int = 8,
        max_loaded_tools: int | None = None,
    ) -> "_ToolsHandle":
        """Build a tool-catalog handle with server-side search via
        ``/sdk/v1/tool-search``.

        ``strategy="all"``      → ``initial`` is the whole catalog; ``search()``
                                  still calls the server.
        ``strategy="deferred"`` → ``initial`` is the ``always_load`` tools plus
                                  the first ``initial_tool_count`` of the catalog
                                  (a subset sent on the first turn); ``search()``
                                  calls the server to load relevant tools on demand.

        Returns a handle with ``.strategy``, ``.initial`` (a ``list[CaveTool]``),
        and ``.search(query, *, max_tools, context, workflow, ranker)`` which
        hits the gateway and returns a :class:`ToolSearchResult`. Mirrors the
        TypeScript ``cave.tools({ catalog, strategy })``.
        """
        if strategy == "all":
            initial = list(catalog)
        else:
            always = [t for t in catalog if t.always_load]
            initial = always + list(catalog[:initial_tool_count])
        return _ToolsHandle(self, catalog, strategy, initial)

    def tool_search(
        self,
        tools: list[CaveTool],
        query: str,
        *,
        context: str | None = None,
        max_tools: int | None = None,
        workflow: str | None = None,
        ranker: str | None = None,
        session_id: str | None = None,
    ) -> ToolSearchResult:
        """POST the tool catalog + query to the gateway's /sdk/v1/tool-search endpoint.

        Returns a ToolSearchResult that includes the reduced tool list and the
        sent_schema_tokens vs full_schema_tokens so callers can measure reduction.

        Args:
            tools: Full tool catalog to filter.
            query: The user's current intent / task description.
            context: Optional additional context for the search.
            max_tools: Optional cap on returned tools (server default applies if omitted).
            workflow: Override workflow label (defaults to Cave.default_workflow).
            ranker: Optional ranking algorithm passed through to the gateway
                ("bm25" default, or "embeddings" when the gateway has an embedding
                provider wired). The SDK passes it through; it never computes
                similarity itself.
            session_id: Optional server-side tool session id for later
                provider-request reinjection.

        Returns:
            ToolSearchResult with .tools, .sent_schema_tokens, .full_schema_tokens,
            .deferred_count, .method, .saved_tokens, .reduction_pct.
        """
        body: dict[str, Any] = {
            "tools": [
                {
                    "name": t.name,
                    "description": t.description,
                    "input_schema": t.input_schema,
                    "read_only": t.read_only,
                    "always_load": t.always_load,
                }
                for t in tools
            ],
            "query": query,
        }
        if context is not None:
            body["context"] = context
        if max_tools is not None:
            body["max_tools"] = max_tools
        if session_id is not None:
            body["session_id"] = session_id
        # Embedding-similarity is a pure passthrough: forward the ranker choice;
        # the gateway honors "embeddings" only when it has an embedding provider
        # wired. The SDK never computes similarity itself (stdlib-only, no deps).
        if ranker is not None:
            body["ranker"] = ranker

        wf = workflow or self.default_workflow
        req = urllib.request.Request(
            f"{self.base_url}/sdk/v1/tool-search",
            data=json.dumps(body).encode(),
            headers=headers(self, wf),
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            data = json.loads(response.read())

        sent = _strict_non_negative_int(data.get("sent_schema_tokens"))
        full = _strict_non_negative_int(data.get("full_schema_tokens"))
        if sent is None or full is None or sent > full:
            sent, full = 0, 0
        return ToolSearchResult(
            tools=data.get("tools", []),
            sent_schema_tokens=sent,
            full_schema_tokens=full,
            deferred_count=_strict_non_negative_int(data.get("deferred_count")) or 0,
            method=data.get("method", "unknown"),
            session_id=data.get("session_id"),
            token_basis=data.get("token_basis") if isinstance(data.get("token_basis"), str) and data.get("token_basis").strip() else "unavailable",
            basis="inferred",
        )

    def compress(self, payload: str, *, content_type: str | None = None) -> CompressResult:
        """Compress a payload through the Engine (``POST /sdk/v1/compress``).

        The SDK is **not** the compressor — it delegates to the Engine and maps
        the report; it never reimplements a compressor (that would fork behavior
        across surfaces).

        **Fail-closed.** On any transport or parse problem the call passes through:
        ``output`` is the original ``payload`` unchanged, ``ratio`` is ``0.0``,
        and there is no ``recovery_handle``. The SDK never rewrites bytes itself
        and never claims a saving it did not get back from the Engine. ``basis``
        is always ``"inferred"``.

        Mirrors the TypeScript ``Cave.compress``.

        Args:
            payload: The raw payload to compress.
            content_type: Optional content-type hint for the Engine's detector.

        Returns:
            A :class:`CompressResult`.
        """

        def passthrough() -> CompressResult:
            return CompressResult(
                output=payload,
                content_type=content_type or "unknown",
                tokens_before=0,
                tokens_after=0,
                ratio=0.0,
                basis="inferred",
                token_count_basis="unavailable",
                recovery_handle=None,
            )

        body: dict[str, Any] = {"input": payload}
        if content_type is not None:
            body["content_type"] = content_type

        req = urllib.request.Request(
            f"{self.base_url}/sdk/v1/compress",
            data=json.dumps(body).encode(),
            headers=headers(self, self.default_workflow),
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=300) as response:
                data = json.loads(response.read())
        except Exception:  # noqa: BLE001 — fail-closed: any failure ⇒ pass-through.
            return passthrough()

        # Anything other than a well-formed report with a string `output` is a
        # parse problem → pass through. Never trust a partial response.
        if not isinstance(data, dict) or not isinstance(data.get("output"), str):
            return passthrough()

        tokens_before = _strict_non_negative_int(data.get("tokens_before"))
        tokens_after = _strict_non_negative_int(data.get("tokens_after"))
        if tokens_before is None or tokens_after is None or tokens_after > tokens_before:
            return passthrough()
        # Ratio is a mathematical derivative of the validated counters. Recompute
        # it rather than preserving an inconsistent/optimistic server field.
        derived_ratio = 0.0 if tokens_before == 0 else (tokens_before - tokens_after) / tokens_before
        ratio = derived_ratio
        if data["output"] == payload and tokens_after != tokens_before:
            return passthrough()
        ct = data.get("content_type")
        handle = data.get("recovery_handle")
        method = data.get("method")
        lossless = data.get("lossless_to_model")
        raw_token_count_basis = data.get("token_count_basis")
        token_count_basis = raw_token_count_basis if isinstance(raw_token_count_basis, str) and raw_token_count_basis.strip() else "unavailable"
        return CompressResult(
            output=data["output"],
            content_type=ct if isinstance(ct, str) else (content_type or "unknown"),
            tokens_before=tokens_before,
            tokens_after=tokens_after,
            ratio=float(ratio),
            basis="inferred",  # honesty: the SDK never emits `verified`.
            token_count_basis=token_count_basis,
            recovery_handle=handle if isinstance(handle, str) else None,
            method=method if isinstance(method, str) else None,
            lossless_to_model=lossless if isinstance(lossless, bool) else None,
        )

    def cave_plan(self) -> dict[str, Any]:
        """Read this project's Cave Plan machine-readably (``GET /sdk/v1/cave-plan``).

        Authed by the project key's ``plan:read`` scope. Returns the
        project-scope plan verbatim as a dict whose keys are the snake_case wire
        fields — identical to control-api and the TypeScript ``cave.cavePlan()``:
        ``headline`` / ``headroom_by_class`` / ``moves`` / ``no_signal`` /
        ``methodology`` / ``scope`` / ``project_id`` (plus optional ``as_of`` /
        ``detectors_last_ran`` / ``diagnostics`` / ``mode_note``). Every dollar
        figure is ``inferred`` and a PER-DAY rate; the SDK passes them through
        verbatim and never re-derives or re-projects them (no monthly, no
        ``verified``).

        This is a control-plane read served by control-api, so it targets
        ``control_url`` (falling back to ``base_url``) and authenticates with the
        ``x-cave-api-key`` header, not the gateway ``authorization: Bearer``
        header. A non-200 raises ``urllib.error.HTTPError`` (mirrors the TS SDK's
        throw) — there is no byte-safe pass-through here.

        Mirrors the TypeScript ``Cave.cavePlan``.
        """
        base = self.control_url or self.base_url
        req = urllib.request.Request(
            f"{base}/sdk/v1/cave-plan",
            headers=otlp_headers(self),
            method="GET",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())


class _SharedContext:
    """Session-keyed multi-agent shared context: one agent ``put``s the full handoff
    context under a session key (``POST /sdk/v1/shared-context``); a peer agent in the
    same project ``get``s it back byte-exact (``GET /sdk/v1/shared-context/{key}``). The
    gateway is tenant-scoped — the project namespaces the key. Mirrors the TS
    ``cave.sharedContext``.
    """

    def __init__(self, cave: "Cave") -> None:
        self._cave = cave

    def put(self, session_key: str, content: str) -> dict[str, Any]:
        """Store ``content`` under ``session_key`` for the authenticated project."""
        return self._call("/sdk/v1/shared-context", {"session_key": session_key, "content": content}, "POST")

    def get(self, session_key: str) -> dict[str, Any]:
        """Recover the full handoff context byte-exact for ``session_key``."""
        return self._call(f"/sdk/v1/shared-context/{quote(session_key, safe='')}", None, "GET")

    def _call(self, path: str, body: dict[str, Any] | None, method: str) -> dict[str, Any]:
        req = urllib.request.Request(
            f"{self._cave.base_url}{path}",
            data=json.dumps(body).encode() if body is not None else None,
            headers=headers(self._cave, self._cave.default_workflow),
            method=method,
        )
        with urllib.request.urlopen(req, timeout=300) as response:
            return json.loads(response.read())


class Trace:
    def __init__(self, cave: Cave, workflow: str, tags: dict[str, str]) -> None:
        self.cave = cave
        self.workflow = workflow
        self.tags = tags
        self.model = {"openai": Provider(cave, "/openai/v1", None, workflow)}

    def tool(self, name: str, options: dict[str, Any], fn: Callable[[], Any]) -> Any:
        start = time.monotonic()
        try:
            return fn()
        finally:
            duration_ms = int((time.monotonic() - start) * 1000)
            try:
                self._request(
                    "/sdk/v1/events",
                    {
                        "span_type": "tool.call",
                        "name": name,
                        "workflow": self.workflow,
                        "options": options,
                        "duration_ms": duration_ms,
                        "tags": self.tags,
                    },
                )
            except Exception:  # noqa: BLE001 — telemetry must never break the agent (mirrors TS .catch).
                pass

    def page_artifact(self, value: Any, options: dict[str, Any]) -> Any:
        """Store a large payload to ``/sdk/v1/artifacts``, returning a compact
        stub the model can later expand.

        ``strategy="verbatim"`` bypasses storage and returns ``value`` unchanged.
        Mirrors the TypeScript ``trace.artifacts.page`` (same wire body, incl.
        ``workflow``, and same stub format). :attr:`artifacts` exposes ``page``
        as the mirror-named entry point; ``page_artifact`` stays as a
        backwards-compatible alias.
        """
        if options.get("strategy") == "verbatim":
            return value
        # `workflow` mirrors the TS SDK body so both SDKs send the identical wire.
        response = self._request(
            "/sdk/v1/artifacts",
            {"value": value, "options": options, "workflow": self.workflow},
        )
        if response.get("stored") is False:
            return value
        content_type = options.get("content_type") or "application/json"
        return (
            f"[cave-artifact id={response['artifact_id']} source={options['source']} type={content_type}]\n"
            f"summary: artifact stored.\n"
            f"retrieve: call cave_expand_artifact with id and json_pointer or range.\n"
            f"[/cave-artifact]"
        )

    @property
    def artifacts(self) -> "_Artifacts":
        """Artifact handle mirroring the TS ``trace.artifacts`` namespace.

        ``trace.artifacts.page(value, options)`` is the mirror-named entry point
        for :meth:`page_artifact`; both hit ``/sdk/v1/artifacts`` with an
        identical wire body.
        """
        return _Artifacts(self)

    def checkpoint(self, messages: list[Any], options: dict[str, Any]) -> dict[str, Any]:
        # `workflow` mirrors the TS SDK body; the gateway's checkpoint.Request
        # carries it (omitempty) so both SDKs send the identical wire.
        return self._request("/sdk/v1/checkpoints", {"messages": messages, "options": options, "workflow": self.workflow})

    def expand(self, source_ref: str) -> dict[str, Any]:
        """Reverse a checkpoint ``source_ref`` back into the original context.

        GETs ``/sdk/v1/checkpoints/{source_ref}/expand``; the gateway returns the
        stored ``{source_ref, version, messages, checkpoint}``. This is the other
        half of :meth:`checkpoint` — reversibility is mandatory: a checkpoint
        that cannot be expanded is a bug. Mirrors the TypeScript
        ``trace.context.expand``.
        """
        return self._get(f"/sdk/v1/checkpoints/{quote(source_ref, safe='')}/expand")

    def _request(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        req = urllib.request.Request(f"{self.cave.base_url}{path}", data=json.dumps(body).encode(), headers=headers(self.cave, self.workflow), method="POST")
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())

    def _get(self, path: str) -> dict[str, Any]:
        req = urllib.request.Request(f"{self.cave.base_url}{path}", headers=headers(self.cave, self.workflow), method="GET")
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())


class Provider:
    def __init__(self, cave: Cave, prefix: str, upstream_key: str | None, workflow: str | None = None) -> None:
        self.cave = cave
        self.prefix = prefix
        self.upstream_key = upstream_key
        self.workflow = workflow or cave.default_workflow
        self.responses = _Create(self, "/responses")
        self.chat = {"completions": _Create(self, "/chat/completions")}

    def create(self, path: str, body: dict[str, Any], *, latency_class: str | None = None, tool_session_id: str | None = None) -> dict[str, Any]:
        req = urllib.request.Request(
            f"{self.cave.base_url}{self.prefix}{path}",
            data=json.dumps(body).encode(),
            headers=headers(self.cave, self.workflow, self.upstream_key, latency_class, tool_session_id),
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=300) as response:
            return json.loads(response.read())

    def raw(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        """Escape hatch: POST a raw body to ``{prefix}{path}`` through the
        gateway with the standard Caveman headers. Mirrors the TS provider
        client's ``raw`` fetch — for native provider paths the typed helpers
        don't cover.
        """
        return self.create(path, body)


class _Create:
    def __init__(self, provider: Provider, path: str) -> None:
        self.provider = provider
        self.path = path

    def create(self, body: dict[str, Any], *, latency_class: str | None = None, tool_session_id: str | None = None) -> dict[str, Any]:
        return self.provider.create(self.path, body, latency_class=latency_class, tool_session_id=tool_session_id)


class _Artifacts:
    """Mirror of the TS ``trace.artifacts`` namespace.

    ``page(value, options)`` delegates to :meth:`Trace.page_artifact` so the two
    SDKs expose the same ``artifacts.page`` capability over the identical wire.
    """

    def __init__(self, trace: "Trace") -> None:
        self._trace = trace

    def page(self, value: Any, options: dict[str, Any]) -> Any:
        return self._trace.page_artifact(value, options)


class _ToolsHandle:
    """A tool-catalog handle returned by :meth:`Cave.tools`.

    Holds the catalog + the computed ``initial`` subset and exposes
    :meth:`search`, which calls the gateway's ``/sdk/v1/tool-search`` with the
    FULL catalog (not just ``initial``). Mirrors the object returned by the TS
    ``cave.tools({ catalog, strategy })``.
    """

    def __init__(self, cave: Cave, catalog: list[CaveTool], strategy: str, initial: list[CaveTool]) -> None:
        self.cave = cave
        self.catalog = catalog
        self.strategy = strategy
        self.initial = initial

    def search(
        self,
        query: str,
        *,
        max_tools: int | None = None,
        context: str | None = None,
        workflow: str | None = None,
        ranker: str | None = None,
        session_id: str | None = None,
    ) -> ToolSearchResult:
        """Search the full catalog via the gateway's ``/sdk/v1/tool-search``
        endpoint. Returns a :class:`ToolSearchResult` with the reduced tool list
        and token counts. ``ranker`` is forwarded verbatim; the SDK never
        computes similarity itself.
        """
        return self.cave.tool_search(
            self.catalog,
            query,
            context=context,
            max_tools=max_tools,
            workflow=workflow,
            ranker=ranker,
            session_id=session_id,
        )


class _Prompts:
    """Prompt-snippet helpers. Mirrors the TS ``cave.prompts`` namespace."""

    def internal_brevity(
        self,
        *,
        style: str,
        preserve_errors_verbatim: bool = False,
        preserve_code_verbatim: bool = False,
    ) -> str:
        """Return an internal output-style instruction snippet.

        ``style`` is one of ``"technical-concise"`` | ``"caveman"`` | ``"none"``;
        ``"none"`` yields the empty string. Mirrors the TS
        ``cave.prompts.internalBrevity`` (same output for the same inputs).
        """
        if style == "none":
            return ""
        # Booleans render lowercase (`true`/`false`) to match the TS SDK's
        # `String(Boolean(...))` output exactly.
        errors = "true" if preserve_errors_verbatim else "false"
        code = "true" if preserve_code_verbatim else "false"
        return (
            f"Internal output style: {style}. "
            f"Preserve errors verbatim: {errors}. "
            f"Preserve code verbatim: {code}."
        )


class AsyncJobsUnavailableError(RuntimeError):
    """Delayed job execution is unavailable and no job was submitted."""

    code = "cave_async_jobs_unavailable"

    def __init__(self) -> None:
        super().__init__(
            "Async job execution is unavailable: durable encrypted request storage, "
            "provider credential custody, and a draining worker are not wired. No job was submitted."
        )


class JobsClient:
    """Reserved async-job surface that fails locally before network I/O."""

    def __init__(self, cave: Cave) -> None:
        self.cave = cave

    def submit(
        self,
        body: dict[str, Any],
        *,
        latency_class: str = "background",
    ) -> Job:
        raise AsyncJobsUnavailableError

    def status(self, job_id: str) -> Job:
        raise AsyncJobsUnavailableError

    def cancel(self, job_id: str) -> dict[str, Any]:
        raise AsyncJobsUnavailableError

    def wait(
        self,
        job_id: str,
        *,
        interval_s: float = 0.5,
        timeout_s: float = 60.0,
    ) -> Job:
        raise AsyncJobsUnavailableError

    def submit_and_wait(
        self,
        body: dict[str, Any],
        *,
        latency_class: str = "background",
        interval_s: float = 0.5,
        timeout_s: float = 60.0,
    ) -> Job:
        raise AsyncJobsUnavailableError


def headers(
    cave: Cave,
    workflow: str,
    upstream_key: str | None = None,
    latency_class: str | None = None,
    tool_session_id: str | None = None,
) -> dict[str, str]:
    data = {
        "content-type": "application/json",
        "authorization": f"Bearer {cave.api_key}",
        "x-cave-agent": cave.agent,
        "x-cave-workflow": workflow,
        "x-cave-retention": cave.retention,
    }
    if cave.user:
        data["x-cave-user-hash"] = cave.user
    if upstream_key:
        data["x-cave-upstream-key"] = upstream_key
    # Async scheduling hint mirrors the TS SDK: anything other than
    # "interactive" is treated as an async/deferrable call. The gateway is free
    # to ignore the header; the SDKs must send the identical wire.
    if latency_class is not None:
        data["x-cave-async"] = "true" if latency_class != "interactive" else "false"
    if tool_session_id:
        data["x-cave-tool-session"] = tool_session_id
    return data


# --- OTel exporter (stdlib-only OTLP/JSON over urllib) ---


@dataclass
class OTelSpan:
    """A single span buffered by :class:`OTelExporter`.

    Mirrors the OTLP/JSON span shape the gateway ingests at
    ``POST /otlp/v1/traces``. GenAI semantic-convention attributes
    (``gen_ai.*``) are produced from the structured fields below; anything in
    ``attributes`` is passed through verbatim.
    """

    name: str
    trace_id: str
    span_id: str
    parent_span_id: str = ""
    kind: int = 3  # SPAN_KIND_CLIENT
    start_time_ns: int = 0
    end_time_ns: int = 0
    status_code: int = 1  # OTLP status: 0=unset, 1=ok, 2=error
    attributes: dict[str, Any] = field(default_factory=dict)

    def to_otlp(self) -> dict[str, Any]:
        return {
            "traceId": self.trace_id,
            "spanId": self.span_id,
            "parentSpanId": self.parent_span_id,
            "name": self.name,
            "kind": self.kind,
            "startTimeUnixNano": str(self.start_time_ns),
            "endTimeUnixNano": str(self.end_time_ns),
            "attributes": [_otlp_kv(k, v) for k, v in self.attributes.items()],
            "status": {"code": self.status_code},
        }


def _otlp_kv(key: str, value: Any) -> dict[str, Any]:
    """Encode one attribute as an OTLP/JSON KeyValue.

    Ints become ``intValue`` (proto3 int64 → JSON string), bools ``boolValue``,
    floats ``doubleValue``; everything else is stringified into ``stringValue``.
    """
    if isinstance(value, bool):
        return {"key": key, "value": {"boolValue": value}}
    if isinstance(value, int):
        return {"key": key, "value": {"intValue": str(value)}}
    if isinstance(value, float):
        return {"key": key, "value": {"doubleValue": value}}
    return {"key": key, "value": {"stringValue": str(value)}}


def _rand_hex(n_bytes: int) -> str:
    return secrets.token_hex(n_bytes)


class OTelExporter:
    """One-call OTel exporter that ships spans to the gateway OTLP endpoint.

    Build with :meth:`Cave.exporter`. Record spans with :meth:`record_span`
    (GenAI fields are mapped to ``gen_ai.*`` semantic-convention attributes),
    then :meth:`export` POSTs the buffered batch to
    ``{base_url}/otlp/v1/traces`` and clears the buffer. Stdlib-only: the
    OTLP/JSON payload is built by hand and sent with ``urllib``.
    """

    def __init__(self, cave: Cave, *, service_name: str | None = None) -> None:
        self.cave = cave
        self.service_name = service_name or cave.agent
        self._spans: list[OTelSpan] = []

    @property
    def pending(self) -> int:
        """Number of spans buffered and not yet exported."""
        return len(self._spans)

    def new_trace_id(self) -> str:
        """A fresh 16-byte (32-hex) trace id."""
        return _rand_hex(16)

    def new_span_id(self) -> str:
        """A fresh 8-byte (16-hex) span id."""
        return _rand_hex(8)

    def record_span(
        self,
        name: str,
        *,
        trace_id: str | None = None,
        span_id: str | None = None,
        parent_span_id: str = "",
        kind: int = 3,
        provider: str | None = None,
        model: str | None = None,
        operation: str | None = None,
        tool_name: str | None = None,
        input_tokens: int | None = None,
        output_tokens: int | None = None,
        cached_tokens: int | None = None,
        cost_usd: float | None = None,
        workflow: str | None = None,
        status: str = "ok",
        start_time_ns: int | None = None,
        end_time_ns: int | None = None,
        attributes: dict[str, Any] | None = None,
    ) -> OTelSpan:
        """Buffer a span, mapping GenAI fields to ``gen_ai.*`` attributes.

        Returns the :class:`OTelSpan` (with generated ids) so callers can chain
        child spans via ``parent_span_id=span.span_id``.
        """
        now_ns = time.time_ns()
        attrs: dict[str, Any] = dict(attributes or {})
        # These values feed spend and savings reports. Only typed, validated
        # arguments may set them; arbitrary attributes cannot overwrite them.
        for reserved in (
            "gen_ai.usage.input_tokens",
            "gen_ai.usage.output_tokens",
            "gen_ai.usage.cached_tokens",
            "gen_ai.usage.cost_usd",
            "cave.agent",
            "cave.workflow",
        ):
            attrs.pop(reserved, None)
        if operation is not None:
            attrs["gen_ai.operation.name"] = operation
        if provider is not None:
            attrs["gen_ai.system"] = provider
        if model is not None:
            attrs["gen_ai.request.model"] = model
            attrs["gen_ai.response.model"] = model
        if tool_name is not None:
            attrs["gen_ai.tool.name"] = tool_name
        valid_input = _strict_non_negative_int(input_tokens)
        valid_output = _strict_non_negative_int(output_tokens)
        valid_cached = _strict_non_negative_int(cached_tokens)
        if valid_input is not None:
            attrs["gen_ai.usage.input_tokens"] = valid_input
        if valid_output is not None:
            attrs["gen_ai.usage.output_tokens"] = valid_output
        if valid_cached is not None and (valid_input is None or valid_cached <= valid_input):
            attrs["gen_ai.usage.cached_tokens"] = valid_cached
        if isinstance(cost_usd, (int, float)) and not isinstance(cost_usd, bool) and math.isfinite(float(cost_usd)) and cost_usd >= 0:
            attrs["gen_ai.usage.cost_usd"] = float(cost_usd)
        attrs["cave.agent"] = self.cave.agent
        attrs["cave.workflow"] = workflow or self.cave.default_workflow

        status_code = {"unset": 0, "ok": 1, "error": 2}.get(status, 1)
        span = OTelSpan(
            name=name,
            trace_id=trace_id or self.new_trace_id(),
            span_id=span_id or self.new_span_id(),
            parent_span_id=parent_span_id,
            kind=kind,
            start_time_ns=start_time_ns if start_time_ns is not None else now_ns,
            end_time_ns=end_time_ns if end_time_ns is not None else now_ns,
            status_code=status_code,
            attributes=attrs,
        )
        self._spans.append(span)
        return span

    def build_payload(self) -> dict[str, Any]:
        """Build the OTLP/JSON payload for the buffered spans (no network)."""
        return {
            "resourceSpans": [
                {
                    "resource": {
                        "attributes": [
                            _otlp_kv("service.name", self.service_name),
                            _otlp_kv("cave.agent", self.cave.agent),
                        ]
                    },
                    "scopeSpans": [
                        {
                            "scope": {"name": "caveman-cloud", "version": "1.0.0"},
                            "spans": [sp.to_otlp() for sp in self._spans],
                        }
                    ],
                }
            ]
        }

    def export(self) -> dict[str, Any]:
        """POST the buffered spans to ``{base_url}/otlp/v1/traces`` and clear them.

        Returns the gateway's JSON response (``ok`` / ``spans_accepted`` /
        ``spans_total`` / ``otel_schema_version``). A no-op returning an empty
        ``ok`` result when there is nothing buffered.
        """
        if not self._spans:
            return {"ok": True, "spans_accepted": 0, "spans_total": 0}
        payload = self.build_payload()
        req = urllib.request.Request(
            f"{self.cave.base_url}/otlp/v1/traces",
            data=json.dumps(payload).encode(),
            headers=otlp_headers(self.cave),
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            data: dict[str, Any] = json.loads(response.read())
        self._spans.clear()
        return data

    # flush is an alias for export, matching common OTel exporter naming.
    flush = export


def otlp_headers(cave: Cave) -> dict[str, str]:
    """Headers for the OTLP trace endpoint.

    Uses ``x-cave-api-key`` (the gateway's primary key header) plus the agent /
    workflow labels the gateway reads on ``/otlp/v1/traces``.
    """
    data = {
        "content-type": "application/json",
        "x-cave-api-key": cave.api_key,
        "x-cave-agent": cave.agent,
        "x-cave-workflow": cave.default_workflow,
        "x-cave-retention": cave.retention,
    }
    if cave.user:
        data["x-cave-user-hash"] = cave.user
    return data
