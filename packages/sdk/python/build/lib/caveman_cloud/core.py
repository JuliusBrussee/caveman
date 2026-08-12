from __future__ import annotations

import base64
import inspect
import json
import hashlib
import math
import os
import secrets
import threading
import time
import urllib.request
from contextlib import contextmanager
from urllib.parse import quote, urlsplit
from dataclasses import dataclass, field
from typing import Any, Callable, Iterator, Literal


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
class ContextPackItem:
    """One caller-owned context fragment considered by ``cave.context.pack``."""

    id: str
    text: str
    tokens: int | None = None
    timestamp: str | None = None
    priority: float | None = None
    pin: bool | None = None


@dataclass
class ContextPackOptions:
    """Budget and scoring options for connected-only context packing."""

    max_tokens: int
    reserve_tokens: int | None = None
    now: str | None = None
    recency_half_life_ms: int | None = None
    recency_weight: float | None = None
    error_boost: float | None = None


@dataclass
class ContextPackResult:
    """Selected caller-owned context plus inferred token accounting."""

    items: list[ContextPackItem]
    tokens_used: int
    tokens_before: int
    tokens_saved: int
    deferred_count: int
    deferred_ids: list[str]
    basis: str = "inferred"


@dataclass
class AssemblySlot:
    """One author-declared request fragment for :meth:`Cave.assemble`."""

    id: str
    stability: str
    content: Any


@dataclass
class AssembleOptions:
    """Input contract for the client-pure cache-optimal request assembler."""

    provider: str
    model: str
    session_id: str
    slots: list[AssemblySlot]
    # "self" places provider hints but mints nothing.
    emit_cache_hints: str = "gateway"


@dataclass
class AssemblyResult:
    """Result from :meth:`Cave.assemble`; every token figure is inferred."""

    request: dict[str, Any]
    headers: dict[str, str]
    prefix_hash: str
    breakpoints: list[str]
    stable_tokens: int
    token_basis: str = "estimated_bytes_div_4"
    basis: str = "inferred"
    volatile_below_breakpoint: bool = True


class AssemblyStabilityError(ValueError):
    """Stable/session content changed inside its declared lifetime."""

    def __init__(self, slot_id: str) -> None:
        self.slot_id = slot_id
        super().__init__(f"assembly slot {slot_id!r} changed after being declared stable")


class _AssembledRequest(dict[str, Any]):
    """Provider request carrying local-only Caveman assembly metadata."""

    assembly_header: str


@dataclass
class CaveTool:
    """A tool descriptor for inclusion in the tool catalog."""
    name: str
    description: str
    input_schema: dict[str, Any]
    read_only: bool = False
    idempotent: bool = False
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

# Runtime-policy constants (see the RuntimePolicyClient section at the bottom of
# this module). Declared here because Cave.runtime_policy defaults to the env name.
_POLICY_KILL_ENV = "CAVEMAN_POLICY_KILL"
_POLICY_PATH = "/sdk/v1/runtime-policy"
_POLICY_SCHEMA_VERSION = "caveman.runtime-policy.v1"
_POLICY_MAX_RESPONSE_BYTES = 1024 * 1024


def _env_workflow() -> str:
    """CAVE_WORKFLOW normalized to the gateway label rule, else the honest default."""
    raw = (os.environ.get("CAVE_WORKFLOW") or "").lower()
    if raw and len(raw) <= 96 and all(c in _WORKFLOW_CHARS for c in raw):
        return raw
    return "unlabeled-workflow"


def _normalized_service_url(value: str, name: str) -> str:
    if value.strip() != value:
        raise ValueError(f"{name} must not contain surrounding whitespace")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except (TypeError, ValueError) as error:
        raise ValueError(f"{name} must be an absolute http(s) URL") from error
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
    ):
        raise ValueError(f"{name} must be an absolute http(s) URL without credentials")
    if parsed.query or parsed.fragment:
        raise ValueError(f"{name} must not contain a query or fragment")
    return value.rstrip("/")


@dataclass
class Cave:
    api_key: str
    base_url: str
    agent: str
    # CAVE_WORKFLOW lets a wrapper (`cave wrap --workflow x`) label every request
    # from an SDK app without a code change. An explicit value always wins. The
    # env value is normalized to the gateway's label rule (lowercase [a-z0-9_-],
    # max 96); an invalid ambient value is ignored rather than 400-ing every
    # request. Mirrors @caveman-ai/sdk (TypeScript).
    default_workflow: str = field(default_factory=lambda: _env_workflow())
    retention: str = "metadata"
    verify_on_init: bool = False
    # control_url is reserved for control-plane APIs and defaults to base_url.
    control_url: str | None = None
    # Opaque end-user identifier (never a Caveman member id); sent as
    # x-cave-user-hash so spend can be grouped by the caller's own users.
    user: str | None = None
    _assembly_hash_ledger: dict[tuple[str, str], str] = field(default_factory=dict, init=False, repr=False)
    _assembly_hash_lock: threading.Lock = field(default_factory=threading.Lock, init=False, repr=False, compare=False)

    def __post_init__(self) -> None:
        self.base_url = _normalized_service_url(self.base_url, "base_url")
        if self.control_url is not None:
            self.control_url = _normalized_service_url(self.control_url, "control_url")

    @contextmanager
    def trace(
        self,
        workflow: str | None = None,
        tags: dict[str, str] | None = None,
        *,
        trace_id: str | None = None,
        span_id: str | None = None,
    ) -> Iterator["Trace"]:
        """Run a block inside a trace.

        The trace allocates a ``trace_id`` (32 lowercase hex) and a root
        ``span_id`` (16 lowercase hex) with the same RNG the OTel exporter uses.
        Every provider call made through the trace carries them as
        ``x-cave-trace-id`` + ``x-cave-parent-span-id``, and ``trace.exporter()``
        reuses the same trace id — so the gateway's request rows and the SDK's
        own spans join into one trace.

        ``trace_id`` / ``span_id`` continue an inbound trace; a value that is not
        the exact non-zero hex shape is replaced by a fresh one rather than put
        on the wire. Mirrors the TypeScript ``Cave.trace``.
        """
        yield Trace(self, workflow or self.default_workflow, tags or {}, trace_id=trace_id, span_id=span_id)

    @property
    def jobs(self) -> "JobsClient":
        """Reserved async-job surface; methods fail locally without network I/O."""
        return JobsClient(self)

    @property
    def shared_context(self) -> "_SharedContext":
        """Session-keyed multi-agent shared context. Mirrors the TS ``cave.sharedContext``."""
        return _SharedContext(self)

    @property
    def context(self) -> "_ContextPacking":
        """Connected-only context selection. Mirrors TypeScript ``cave.context``."""
        return _ContextPacking(self)

    @property
    def prompts(self) -> "_Prompts":
        """Prompt-snippet helpers. Mirrors the TS ``cave.prompts`` namespace."""
        return _Prompts()

    def retry_loop_breaker(self, threshold: int = 3) -> RetryLoopBreaker:
        """A fresh :class:`RetryLoopBreaker` that interrupts a repeated identical
        tool-call loop after ``threshold`` consecutive repeats.
        """
        return RetryLoopBreaker(threshold=threshold)

    def assemble(self, options: AssembleOptions) -> AssemblyResult:
        """Build a provider request with stable/session content above volatile.

        Pure client-side: no account and no network call. The hash ledger is
        in-process and scoped by ``session_id``; deterministic slot content
        across processes remains the builder's obligation.

        ``emit_cache_hints="gateway"`` (default) emits no provider hint so the
        gateway optimizer retains attribution. ``"self"`` places provider
        hints for direct calls but mints nothing.
        """
        provider = options.provider.strip().lower()
        if not options.model or not options.session_id:
            raise ValueError("assemble requires model and session_id")
        if options.emit_cache_hints not in {"gateway", "self", "none"}:
            raise ValueError(f"unknown emit_cache_hints value {options.emit_cache_hints!r}")

        ids: set[str] = set()
        normalized: list[AssemblySlot] = []
        for slot in options.slots:
            if not slot.id or slot.id in ids:
                raise ValueError(f"assembly slot id must be non-empty and unique: {slot.id!r}")
            ids.add(slot.id)
            if slot.stability not in {"stable", "session", "volatile"}:
                raise ValueError(
                    f"assembly slot {slot.id!r} has unknown stability {slot.stability!r}"
                )
            try:
                content_json = _canonical_json(slot.content)
                content = json.loads(content_json)
            except (TypeError, ValueError):
                raise ValueError(
                    f"assembly slot {slot.id!r} content is not JSON-serializable"
                ) from None
            if slot.stability != "volatile":
                key = (options.session_id, slot.id)
                content_hash = _sha256_hex(content_json)
                with self._assembly_hash_lock:
                    previous = self._assembly_hash_ledger.get(key)
                    if previous is not None and previous != content_hash:
                        raise AssemblyStabilityError(slot.id)
                    self._assembly_hash_ledger[key] = content_hash
            normalized.append(AssemblySlot(slot.id, slot.stability, content))

        ordered = [
            slot
            for stability in ("stable", "session", "volatile")
            for slot in normalized
            if slot.stability == stability
        ]
        prefix_slots = [slot for slot in ordered if slot.stability != "volatile"]
        volatile_slots = [slot for slot in ordered if slot.stability == "volatile"]
        breakpoints: list[str] = []

        if provider == "anthropic":
            tools_slot = next(
                (slot for slot in prefix_slots if slot.id == "tools" and isinstance(slot.content, list)),
                None,
            )
            system_slots = [slot for slot in prefix_slots if slot is not tools_slot]
            system = [
                {"type": "text", "text": _assembly_text(slot.content)}
                for slot in system_slots
            ]
            messages: list[dict[str, Any]] = (
                [
                    {
                        "role": "user",
                        "content": [
                            {"type": "text", "text": _assembly_text(slot.content)}
                            for slot in volatile_slots
                        ],
                    }
                ]
                if volatile_slots
                else []
            )
            raw_request: dict[str, Any] = {"model": options.model}
            prefix: dict[str, Any] = {"model": options.model}
            if tools_slot is not None:
                raw_request["tools"] = _clone_canonical(tools_slot.content)
                prefix["tools"] = tools_slot.content
            if system:
                raw_request["system"] = system
                prefix["system"] = system
            raw_request["messages"] = messages
            prefix_bytes = _canonical_json(prefix)

            if options.emit_cache_hints == "self":
                tools = raw_request.get("tools")
                if isinstance(tools, list):
                    for index in range(len(tools) - 1, -1, -1):
                        tool = tools[index]
                        if isinstance(tool, dict):
                            tool["cache_control"] = {"type": "ephemeral"}
                            breakpoints = [f"tools[{index}]"]
                            break
                if not breakpoints:
                    blocks = raw_request.get("system")
                    if isinstance(blocks, list):
                        for index in range(len(blocks) - 1, -1, -1):
                            block = blocks[index]
                            if isinstance(block, dict):
                                block["cache_control"] = {"type": "ephemeral"}
                                breakpoints = [f"system[{index}]"]
                                break
        elif provider == "openai":
            tools_slot = next(
                (slot for slot in prefix_slots if slot.id == "tools" and isinstance(slot.content, list)),
                None,
            )
            system_slots = [slot for slot in prefix_slots if slot is not tools_slot]
            system_text = "\n\n".join(_assembly_text(slot.content) for slot in system_slots)
            messages = []
            if system_text:
                messages.append({"role": "system", "content": system_text})
            messages.extend(
                {"role": "user", "content": _assembly_text(slot.content)}
                for slot in volatile_slots
            )
            raw_request = {"model": options.model}
            if tools_slot is not None:
                raw_request["tools"] = _clone_canonical(tools_slot.content)
            raw_request["messages"] = messages
            signature_parts = [f"model={options.model}"]
            if tools_slot is not None:
                signature_parts.append(_canonical_json(tools_slot.content))
            if system_text:
                signature_parts.append(_canonical_json(system_text))
            prefix_bytes = "\0".join(signature_parts)
            if options.emit_cache_hints == "self" and (
                tools_slot is not None or system_text
            ):
                raw_request["prompt_cache_key"] = _sha256_hex(prefix_bytes)[:32]
                breakpoints = ["prompt_cache_key"]
        else:
            raw_request = {
                "model": options.model,
                "slots": [
                    {
                        "id": slot.id,
                        "stability": slot.stability,
                        "content": _clone_canonical(slot.content),
                    }
                    for slot in ordered
                ],
            }
            prefix_bytes = _canonical_json(
                [
                    {
                        "id": slot.id,
                        "stability": slot.stability,
                        "content": slot.content,
                    }
                    for slot in prefix_slots
                ]
            )

        prefix_hash = _sha256_hex(prefix_bytes)
        assembly_header = (
            f"v1;slots={len(ordered)};prefix={prefix_hash[:12]};vbb=1"
        )
        request = _AssembledRequest(raw_request)
        request.assembly_header = assembly_header
        return AssemblyResult(
            request=request,
            headers={"x-cave-assembly": assembly_header},
            prefix_hash=prefix_hash,
            breakpoints=breakpoints,
            stable_tokens=(len(prefix_bytes.encode("utf-8")) + 3) // 4,
        )

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
        (``POST {base_url}/v1/traces``) with Caveman headers
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
        if strategy not in {"all", "deferred"}:
            raise ValueError("strategy must be all or deferred")
        if isinstance(initial_tool_count, bool) or not isinstance(initial_tool_count, int) or initial_tool_count < 0:
            raise ValueError("initial_tool_count must be a non-negative integer")
        if max_loaded_tools is not None and (isinstance(max_loaded_tools, bool) or not isinstance(max_loaded_tools, int) or max_loaded_tools < 1):
            raise ValueError("max_loaded_tools must be a positive integer")
        if strategy == "all":
            initial = list(catalog)
        else:
            always = [t for t in catalog if t.always_load]
            if max_loaded_tools is not None and len(always) > max_loaded_tools:
                raise ValueError("max_loaded_tools must be at least the always_load tool count")
            lazy = [t for t in catalog if not t.always_load]
            lazy_limit = initial_tool_count if max_loaded_tools is None else min(initial_tool_count, max_loaded_tools - len(always))
            initial = always + lazy[:lazy_limit]
        return _ToolsHandle(self, catalog, strategy, initial, max_loaded_tools)

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
        if max_tools is not None and (isinstance(max_tools, bool) or not isinstance(max_tools, int) or max_tools < 1):
            raise ValueError("max_tools must be a positive integer")
        body: dict[str, Any] = {
            "tools": [
                {
                    "name": t.name,
                    "description": t.description,
                    "input_schema": t.input_schema,
                    "read_only": t.read_only,
                    "idempotent": t.idempotent,
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

        This is a gateway-owned project-key read, so it always targets
        ``base_url``. ``control_url`` is reserved for control-api ``/api/v1/*``
        surfaces and cannot redirect this method to a route that service lacks.
        A non-200 raises ``urllib.error.HTTPError`` (mirrors the TS SDK's throw)
        — there is no byte-safe pass-through here.

        Mirrors the TypeScript ``Cave.cavePlan``.
        """
        req = urllib.request.Request(
            f"{self.base_url}/sdk/v1/cave-plan",
            headers=otlp_headers(self),
            method="GET",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())

    def runtime_policy(
        self,
        *,
        public_key: str | None = None,
        auto_refresh_seconds: float | None = None,
        kill_env: str = _POLICY_KILL_ENV,
        workflow: str | None = None,
    ) -> "RuntimePolicyClient":
        """A local runtime-policy client for this project.

        The client pulls the project's published policy bundle
        (``GET /sdk/v1/runtime-policy``) and answers :meth:`RuntimePolicyClient.decide`
        locally — no network call, no blocking, on the agent's hot path. Mirrors
        the TypeScript ``cave.runtimePolicy``.

        Args:
            public_key: Optional base64 raw 32-byte Ed25519 key to pin. When set,
                a bundle MUST carry a signature that verifies against it. A value
                that is not a base64 32-byte key raises :class:`ValueError` here
                rather than yielding a client that rejects every bundle.
            auto_refresh_seconds: Optional background refresh interval (daemon
                thread). Off by default; the caller otherwise drives
                :meth:`RuntimePolicyClient.refresh`.
            kill_env: Environment variable consulted on every decision as a local
                brake (default ``CAVEMAN_POLICY_KILL``).
            workflow: Workflow label for the refresh request (defaults to
                ``Cave.default_workflow``).
        """
        return RuntimePolicyClient(
            self,
            public_key=public_key,
            auto_refresh_seconds=auto_refresh_seconds,
            kill_env=kill_env,
            workflow=workflow,
        )


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


class _ContextPacking:
    """Connected-only context selector over caller-owned items.

    ``pack`` decides what enters the model window. Cache-optimal assembly
    decides where selected content sits relative to a cache breakpoint; callers
    may use both. Packing never writes CCR and never runs in local wrap.
    """

    def __init__(self, cave: "Cave") -> None:
        self._cave = cave

    def pack(
        self,
        query: str,
        items: list[ContextPackItem],
        options: ContextPackOptions,
    ) -> ContextPackResult:
        """POST context candidates to ``/sdk/v1/context/pack``.

        On transport or malformed-report failure, returns every caller-owned
        item with zero inferred savings and no deferred IDs.
        """

        def passthrough() -> ContextPackResult:
            return ContextPackResult(
                items=list(items),
                tokens_used=0,
                tokens_before=0,
                tokens_saved=0,
                deferred_count=0,
                deferred_ids=[],
                basis="inferred",
            )

        if isinstance(options.max_tokens, bool) or not isinstance(options.max_tokens, int) or options.max_tokens <= 0:
            return passthrough()
        by_id: dict[str, ContextPackItem] = {}
        for item in items:
            if not item.id or item.id in by_id:
                return passthrough()
            by_id[item.id] = item

        wire_items: list[dict[str, Any]] = []
        for item in items:
            wire: dict[str, Any] = {"id": item.id, "text": item.text}
            for field_name in ("tokens", "timestamp", "priority", "pin"):
                value = getattr(item, field_name)
                if value is not None:
                    wire[field_name] = value
            wire_items.append(wire)

        wire_options: dict[str, Any] = {"max_tokens": options.max_tokens}
        for field_name in (
            "reserve_tokens",
            "now",
            "recency_half_life_ms",
            "recency_weight",
            "error_boost",
        ):
            value = getattr(options, field_name)
            if value is not None:
                wire_options[field_name] = value

        req = urllib.request.Request(
            f"{self._cave.base_url}/sdk/v1/context/pack",
            data=json.dumps({"query": query, "items": wire_items, "options": wire_options}).encode(),
            headers=headers(self._cave, self._cave.default_workflow),
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                data = json.loads(response.read())
        except Exception:  # noqa: BLE001 — lossy selector must fail closed to caller-owned context.
            return passthrough()

        if not isinstance(data, dict):
            return passthrough()
        raw_items = data.get("items")
        raw_deferred = data.get("deferred_ids")
        if not isinstance(raw_items, list) or not isinstance(raw_deferred, list):
            return passthrough()

        selected_ids: list[str] = []
        selected_set: set[str] = set()
        for raw in raw_items:
            if not isinstance(raw, dict):
                return passthrough()
            item_id = raw.get("id")
            if not isinstance(item_id, str) or item_id not in by_id or item_id in selected_set:
                return passthrough()
            selected_ids.append(item_id)
            selected_set.add(item_id)

        expected_deferred = [item.id for item in items if item.id not in selected_set]
        if (
            not all(isinstance(item_id, str) for item_id in raw_deferred)
            or raw_deferred != expected_deferred
        ):
            return passthrough()

        tokens_used = _strict_non_negative_int(data.get("tokens_used"))
        tokens_before = _strict_non_negative_int(data.get("tokens_before"))
        tokens_saved = _strict_non_negative_int(data.get("tokens_saved"))
        deferred_count = _strict_non_negative_int(data.get("deferred_count"))
        if (
            tokens_used is None
            or tokens_before is None
            or tokens_saved is None
            or deferred_count is None
            or tokens_used + tokens_saved != tokens_before
            or deferred_count != len(raw_deferred)
        ):
            return passthrough()

        return ContextPackResult(
            items=[by_id[item_id] for item_id in selected_ids],
            tokens_used=tokens_used,
            tokens_before=tokens_before,
            tokens_saved=tokens_saved,
            deferred_count=deferred_count,
            deferred_ids=list(raw_deferred),
            basis="inferred",
        )


class Trace:
    def __init__(
        self,
        cave: Cave,
        workflow: str,
        tags: dict[str, str],
        *,
        trace_id: str | None = None,
        span_id: str | None = None,
    ) -> None:
        self.cave = cave
        self.workflow = workflow
        self.tags = tags
        # Trace id shared by every provider call and exported span in this trace;
        # span_id is this trace's root span, sent as x-cave-parent-span-id.
        self.trace_id = _normalize_trace_id(trace_id)
        self.span_id = _normalize_span_id(span_id)
        self._exporters: dict[str, OTelExporter] = {}
        self._exporters_lock = threading.Lock()
        self._tool_sequence = 0
        self._tool_sequence_lock = threading.Lock()
        self.model = {
            "openai": Provider(cave, "/openai/v1", None, workflow, trace_id=self.trace_id, parent_span_id=self.span_id)
        }

    def exporter(self, *, service_name: str | None = None) -> "OTelExporter":
        """An OTel exporter bound to this trace: spans recorded without an
        explicit ``trace_id`` inherit this trace's, so SDK spans and the
        gateway's request rows land in the same trace. Repeated calls for one
        service return the same buffer, including runtime-policy decision
        spans, so callers can flush them. Mirrors the TypeScript
        ``CaveTrace.exporter``.
        """
        resolved_service_name = service_name or self.cave.agent
        with self._exporters_lock:
            exporter = self._exporters.get(resolved_service_name)
            if exporter is None:
                exporter = OTelExporter(
                    self.cave,
                    service_name=resolved_service_name,
                    default_trace_id=self.trace_id,
                )
                self._exporters[resolved_service_name] = exporter
            return exporter

    def tool(self, name: str, options: dict[str, Any], fn: Callable[[], Any]) -> Any:
        with self._tool_sequence_lock:
            self._tool_sequence += 1
            sequence = self._tool_sequence
        start_ns = time.monotonic_ns()
        try:
            result = fn()
        except BaseException:
            self._emit_tool_event(name, options, "error", sequence, start_ns)
            raise

        if inspect.isawaitable(result):
            async def complete_awaitable() -> Any:
                try:
                    value = await result
                except BaseException:
                    self._emit_tool_event(name, options, "error", sequence, start_ns)
                    raise
                self._emit_tool_event(name, options, "ok", sequence, start_ns)
                return value

            return complete_awaitable()

        self._emit_tool_event(name, options, "ok", sequence, start_ns)
        return result

    def _emit_tool_event(
        self,
        name: str,
        options: dict[str, Any],
        outcome: Literal["ok", "error"],
        sequence: int,
        start_ns: int,
    ) -> None:
        duration_ms = max(0, (time.monotonic_ns() - start_ns) // 1_000_000)
        try:
            self._request(
                "/sdk/v1/events",
                {
                    "span_type": "tool.call",
                    "name": name,
                    "workflow": self.workflow,
                    "options": options,
                    "outcome": outcome,
                    "sequence": sequence,
                    "duration_ms": duration_ms,
                    "tags": self.tags,
                },
            )
        except BaseException:  # noqa: BLE001 — telemetry must never replace a tool result/error.
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
            extra_headers={
                "x-cave-artifact-envelope": "value-v1",
                "x-cave-artifact-source": str(options["source"]),
            },
        )
        if response.get("stored") is False:
            return value
        if response.get("stored") is not True or not isinstance(response.get("artifact_id"), str) or not response["artifact_id"].strip():
            raise RuntimeError("cave artifact response missing artifact_id")
        content_type = options.get("content_type") or "application/json"
        return (
            f"[cave-artifact id={response['artifact_id']} source={options['source']} type={content_type}]\n"
            f"summary: artifact stored.\n"
            f"retrieve: authenticated GET /sdk/v1/artifacts/{response['artifact_id']} or trace.artifacts.get(\"{response['artifact_id']}\").\n"
            f"[/cave-artifact]"
        )

    def get_artifact(self, artifact_id: str) -> Any:
        """Retrieve and JSON-decode one artifact produced by :meth:`page_artifact`."""
        if not isinstance(artifact_id, str) or not artifact_id.strip():
            raise ValueError("artifact_id is required")
        return self._get(f"/sdk/v1/artifacts/{quote(artifact_id, safe='')}")

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

    def _request(self, path: str, body: dict[str, Any], *, extra_headers: dict[str, str] | None = None) -> dict[str, Any]:
        request_headers = headers(
            self.cave,
            self.workflow,
            trace_id=self.trace_id,
            parent_span_id=self.span_id,
        )
        if extra_headers:
            request_headers.update(extra_headers)
        req = urllib.request.Request(
            f"{self.cave.base_url}{path}",
            data=json.dumps(body).encode(),
            headers=request_headers,
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())

    def _get(self, path: str) -> dict[str, Any]:
        req = urllib.request.Request(
            f"{self.cave.base_url}{path}",
            headers=headers(
                self.cave,
                self.workflow,
                trace_id=self.trace_id,
                parent_span_id=self.span_id,
            ),
            method="GET",
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.loads(response.read())


class Provider:
    def __init__(
        self,
        cave: Cave,
        prefix: str,
        upstream_key: str | None,
        workflow: str | None = None,
        *,
        trace_id: str | None = None,
        parent_span_id: str | None = None,
    ) -> None:
        self.cave = cave
        self.prefix = prefix
        self.upstream_key = upstream_key
        self.workflow = workflow or cave.default_workflow
        # Set only for providers reached through a Trace; a bare Cave.openai()
        # client carries no trace continuity headers (mirrors the TS SDK).
        self.trace_id = trace_id
        self.parent_span_id = parent_span_id
        self.responses = _Create(self, "/responses")
        self.chat = {"completions": _Create(self, "/chat/completions")}

    def create(self, path: str, body: dict[str, Any], *, latency_class: str | None = None, tool_session_id: str | None = None) -> dict[str, Any]:
        assembly_header = getattr(body, "assembly_header", None)
        req = urllib.request.Request(
            f"{self.cave.base_url}{self.prefix}{path}",
            data=json.dumps(body).encode(),
            headers=headers(
                self.cave,
                self.workflow,
                self.upstream_key,
                latency_class,
                tool_session_id,
                assembly_header=assembly_header,
                trace_id=self.trace_id,
                parent_span_id=self.parent_span_id,
            ),
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

    def get(self, artifact_id: str) -> Any:
        return self._trace.get_artifact(artifact_id)


class _ToolsHandle:
    """A tool-catalog handle returned by :meth:`Cave.tools`.

    Holds the catalog + the computed ``initial`` subset and exposes
    :meth:`search`, which calls the gateway's ``/sdk/v1/tool-search`` with the
    FULL catalog (not just ``initial``). Mirrors the object returned by the TS
    ``cave.tools({ catalog, strategy })``.
    """

    def __init__(self, cave: Cave, catalog: list[CaveTool], strategy: str, initial: list[CaveTool], max_loaded_tools: int | None) -> None:
        self.cave = cave
        self.catalog = catalog
        self.strategy = strategy
        self.initial = initial
        self.max_loaded_tools = max_loaded_tools

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
            max_tools=max_tools if max_tools is not None else self.max_loaded_tools,
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
    *,
    assembly_header: str | None = None,
    trace_id: str | None = None,
    parent_span_id: str | None = None,
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
    if assembly_header:
        data["x-cave-assembly"] = assembly_header
    # Trace continuity: only requests made through a Trace carry these, so the
    # gateway row's span parents onto the SDK's root span.
    if trace_id and parent_span_id:
        data["x-cave-trace-id"] = trace_id
        data["x-cave-parent-span-id"] = parent_span_id
    return data


def _canonical_json(value: Any) -> str:
    """Stable compact UTF-8 JSON shared by assembly hashes and cloned output."""
    return json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        allow_nan=False,
    )


def _clone_canonical(value: Any) -> Any:
    return json.loads(_canonical_json(value))


def _assembly_text(value: Any) -> str:
    return value if isinstance(value, str) else _canonical_json(value)


def _sha256_hex(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


# --- OTel exporter (stdlib-only OTLP/JSON over urllib) ---


@dataclass
class OTelSpan:
    """A single span buffered by :class:`OTelExporter`.

    Mirrors OTLP/JSON span shape gateway ingests at
    ``POST /v1/traces``. GenAI semantic-convention attributes
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


_HEX_CHARS = set("0123456789abcdef")


def _is_hex_id(value: Any, hex_len: int) -> bool:
    return isinstance(value, str) and len(value) == hex_len and all(c in _HEX_CHARS for c in value)


def _normalize_trace_id(value: str | None) -> str:
    """A caller-supplied trace id, canonicalized to lowercase, or a fresh one
    when it is not exactly 32 hex characters and non-zero. An invalid id is never put on
    the wire — the gateway would drop it anyway, and a malformed id silently
    breaks the join. Mirrors the TypeScript ``normalizeTraceId``.
    """
    normalized = value.lower() if isinstance(value, str) else ""
    return normalized if _is_hex_id(normalized, 32) and any(c != "0" for c in normalized) else _rand_hex(16)


def _normalize_span_id(value: str | None) -> str:
    """A caller-supplied span id, canonicalized to lowercase, or a fresh valid id."""
    normalized = value.lower() if isinstance(value, str) else ""
    return normalized if _is_hex_id(normalized, 16) and any(c != "0" for c in normalized) else _rand_hex(8)


def _normalize_parent_span_id(value: str | None) -> str:
    """Canonicalize a parent id; malformed values become root spans because causality cannot be regenerated."""
    normalized = value.lower() if isinstance(value, str) else ""
    return normalized if _is_hex_id(normalized, 16) and any(c != "0" for c in normalized) else ""


class OTelExporter:
    """One-call OTel exporter that ships spans to the gateway OTLP endpoint.

    Build with :meth:`Cave.exporter`. Record spans with :meth:`record_span`
    (GenAI fields are mapped to ``gen_ai.*`` semantic-convention attributes),
    then :meth:`export` POSTs the buffered batch to
    ``{base_url}/v1/traces`` and clears the buffer. Stdlib-only: the
    OTLP/JSON payload is built by hand and sent with ``urllib``.
    """

    def __init__(self, cave: Cave, *, service_name: str | None = None, default_trace_id: str | None = None) -> None:
        self.cave = cave
        self.service_name = service_name or cave.agent
        # Trace id inherited by spans recorded without an explicit trace_id;
        # set by Trace.exporter() so SDK spans join the gateway's request rows.
        self.default_trace_id = default_trace_id
        self._spans: list[OTelSpan] = []
        self._spans_lock = threading.Lock()
        self._export_lock = threading.Lock()
        self._inflight_spans = 0

    @property
    def pending(self) -> int:
        """Number of spans buffered and not yet exported."""
        with self._spans_lock:
            return len(self._spans) + self._inflight_spans

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
            "gen_ai.usage.cache_read.input_tokens",
            "gen_ai.usage.cost_usd",
            "cave.agent",
            "cave.workflow",
        ):
            attrs.pop(reserved, None)
        if operation is not None:
            attrs["gen_ai.operation.name"] = operation
        if provider is not None:
            attrs["gen_ai.provider.name"] = provider
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
            attrs["gen_ai.usage.cache_read.input_tokens"] = valid_cached
        if isinstance(cost_usd, (int, float)) and not isinstance(cost_usd, bool) and math.isfinite(float(cost_usd)) and cost_usd >= 0:
            attrs["gen_ai.usage.cost_usd"] = float(cost_usd)
        attrs["cave.agent"] = self.cave.agent
        attrs["cave.workflow"] = workflow or self.cave.default_workflow

        status_code = 1 if status is None else {"unset": 0, "ok": 1, "error": 2}.get(status, 0)
        span = OTelSpan(
            name=name,
            trace_id=_normalize_trace_id(trace_id or self.default_trace_id),
            span_id=_normalize_span_id(span_id),
            parent_span_id=_normalize_parent_span_id(parent_span_id),
            kind=kind,
            start_time_ns=start_time_ns if start_time_ns is not None else now_ns,
            end_time_ns=end_time_ns if end_time_ns is not None else now_ns,
            status_code=status_code,
            attributes=attrs,
        )
        with self._spans_lock:
            self._spans.append(span)
        return span

    def build_payload(self) -> dict[str, Any]:
        """Build the OTLP/JSON payload for the buffered spans (no network)."""
        with self._spans_lock:
            spans = list(self._spans)
        return self._build_payload(spans)

    def _build_payload(self, spans: list[OTelSpan]) -> dict[str, Any]:
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
                            "spans": [sp.to_otlp() for sp in spans],
                        }
                    ],
                }
            ]
        }

    def export(self) -> dict[str, Any]:
        """POST buffered spans to standard OTLP/HTTP ``{base_url}/v1/traces``.

        Returns the gateway's JSON response (``ok`` / ``spans_accepted`` /
        ``spans_total`` / ``otel_schema_version``). A no-op returning an empty
        ``ok`` result when there is nothing buffered.
        """
        with self._export_lock:
            with self._spans_lock:
                if not self._spans:
                    return {"ok": True, "spans_accepted": 0, "spans_total": 0}
                batch = self._spans
                self._spans = []
                self._inflight_spans += len(batch)
            succeeded = False
            try:
                req = urllib.request.Request(
                    f"{self.cave.base_url}/v1/traces",
                    data=json.dumps(self._build_payload(batch)).encode(),
                    headers=otlp_headers(self.cave),
                    method="POST",
                )
                with urllib.request.urlopen(req, timeout=30) as response:
                    data: dict[str, Any] = json.loads(response.read())
                succeeded = True
                return data
            finally:
                with self._spans_lock:
                    self._inflight_spans -= len(batch)
                    if not succeeded:
                        self._spans = batch + self._spans

    # flush is an alias for export, matching common OTel exporter naming.
    flush = export


def otlp_headers(cave: Cave) -> dict[str, str]:
    """Headers for the OTLP trace endpoint.

    Uses ``x-cave-api-key`` (the gateway's primary key header) plus the agent /
    workflow labels gateway reads on ``/v1/traces``.
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


# --- Ed25519 signature verification (RFC 8032, verification only) ------------
#
# The stdlib ships no Ed25519. The SDK has no third-party dependencies, so the
# reference verification construction lives here: point decompression, scalar
# multiplication on the twisted-Edwards form of Curve25519, and the
# `[S]B == R + [k]A` check. There is NO signing here and no secret ever touches
# this code — a signature is checked once per policy refresh, so clarity beats
# speed throughout.

_ED_P = 2**255 - 19
_ED_L = 2**252 + 27742317777372353535851937790883648493
_ED_D = (-121665 * pow(121666, _ED_P - 2, _ED_P)) % _ED_P
_ED_I = pow(2, (_ED_P - 1) // 4, _ED_P)


def _ed_recover_x(y: int, sign: int) -> int | None:
    """The x coordinate for a compressed point, or None when y is off-curve."""
    if y >= _ED_P:
        return None
    xx = (y * y - 1) * pow(_ED_D * y * y + 1, _ED_P - 2, _ED_P) % _ED_P
    x = pow(xx, (_ED_P + 3) // 8, _ED_P)
    if (x * x - xx) % _ED_P != 0:
        x = (x * _ED_I) % _ED_P
    if (x * x - xx) % _ED_P != 0:
        return None
    if x == 0 and sign == 1:
        return None
    if x % 2 != sign:
        x = _ED_P - x
    return x


# Extended (X, Y, Z, T) coordinates: additions stay in projective space so a
# scalar multiplication needs a single inversion at the end.
def _ed_add(pt1: tuple[int, int, int, int], pt2: tuple[int, int, int, int]) -> tuple[int, int, int, int]:
    (x1, y1, z1, t1) = pt1
    (x2, y2, z2, t2) = pt2
    a = (y1 - x1) * (y2 - x2) % _ED_P
    b = (y1 + x1) * (y2 + x2) % _ED_P
    c = t1 * 2 * _ED_D * t2 % _ED_P
    dd = z1 * 2 * z2 % _ED_P
    e = b - a
    f = dd - c
    g = dd + c
    h = b + a
    return (e * f % _ED_P, g * h % _ED_P, f * g % _ED_P, e * h % _ED_P)


def _ed_double(pt: tuple[int, int, int, int]) -> tuple[int, int, int, int]:
    (x1, y1, z1, _t1) = pt
    a = x1 * x1 % _ED_P
    b = y1 * y1 % _ED_P
    c = 2 * z1 * z1 % _ED_P
    h = a + b
    e = h - (x1 + y1) * (x1 + y1) % _ED_P
    g = a - b
    f = c + g
    return (e * f % _ED_P, g * h % _ED_P, f * g % _ED_P, e * h % _ED_P)


def _ed_scalarmult(pt: tuple[int, int, int, int], scalar: int) -> tuple[int, int, int, int]:
    result = (0, 1, 1, 0)  # the neutral element
    while scalar > 0:
        if scalar & 1:
            result = _ed_add(result, pt)
        pt = _ed_double(pt)
        scalar >>= 1
    return result


def _ed_base_point() -> tuple[int, int, int, int]:
    by = 4 * pow(5, _ED_P - 2, _ED_P) % _ED_P
    bx = _ed_recover_x(by, 0)
    assert bx is not None  # the RFC 8032 base point is on the curve by construction
    return (bx, by, 1, bx * by % _ED_P)


_ED_B = _ed_base_point()


def _ed_encode(pt: tuple[int, int, int, int]) -> bytes:
    (x, y, z, _t) = pt
    zi = pow(z, _ED_P - 2, _ED_P)
    x = x * zi % _ED_P
    y = y * zi % _ED_P
    return (y | ((x & 1) << 255)).to_bytes(32, "little")


def _ed_decode(data: bytes) -> tuple[int, int, int, int] | None:
    if len(data) != 32:
        return None
    y = int.from_bytes(data, "little") & ((1 << 255) - 1)
    x = _ed_recover_x(y, (data[31] >> 7) & 1)
    if x is None:
        return None
    # -x² + y² = 1 + d·x²·y² is the curve; a point that misses it is rejected.
    if (y * y - x * x - 1 - _ED_D * x * x * y * y) % _ED_P != 0:
        return None
    return (x, y, 1, x * y % _ED_P)


def _ed25519_verify(signature: bytes, message: bytes, public_key: bytes) -> bool:
    """True when ``signature`` is a valid Ed25519 signature of ``message``.

    Verification only — this function never signs and holds no secret. Any
    malformed input returns False (fail closed), never an exception.

    Deliberately cofactorless (no multiply-by-8 check) and it does NOT reject
    small-order or identity public keys: the signer is Go's ``crypto/ed25519``,
    and this verifier must accept exactly what that library produces and
    rejects. "Hardening" it here would make the SDK disagree with the signer on
    valid bundles, which is a worse failure than the (unreachable-in-practice)
    small-order key it would exclude. Change it only alongside the Go side.
    """
    try:
        if len(signature) != 64 or len(public_key) != 32:
            return False
        r_point = _ed_decode(signature[:32])
        a_point = _ed_decode(public_key)
        if r_point is None or a_point is None:
            return False
        s = int.from_bytes(signature[32:], "little")
        if s >= _ED_L:  # non-canonical S: reject rather than reduce.
            return False
        k = int.from_bytes(hashlib.sha512(signature[:32] + public_key + message).digest(), "little") % _ED_L
        return _ed_encode(_ed_scalarmult(_ED_B, s)) == _ed_encode(_ed_add(r_point, _ed_scalarmult(a_point, k)))
    except Exception:  # noqa: BLE001 — a verifier must fail closed, never raise.
        return False


# --- Runtime policy client (spec §21.2) --------------------------------------


def policy_unit_fraction(*keys: str) -> float:
    """Deterministic [0,1) fraction for a tuple of keys — the experiment
    assignment unit.

    Byte-for-byte port of the Go ``shared/platform/sampling.Fraction``: one
    SHA-256 over each key preceded by an 8-byte big-endian prefix carrying the
    key's UTF-8 byte length, then the first 8 digest bytes read big-endian,
    shifted right 11 and divided by 2^53. The length prefix is what keeps
    ``("ab","c")`` and ``("a","bc")`` distinct.

    Public so a caller can reproduce an assignment without a decision, and so
    the cross-language fixtures can pin this port bit-for-bit. Mirrors the
    TypeScript ``policyUnitFraction``; the ``assignment_vectors`` in
    ``public/sdk/parity/runtime-policy.fixtures.json`` are the authority for
    every value this returns (exact float equality, including the empty-unit-key
    vector that :meth:`RuntimePolicyClient.decide` refuses to assign on).
    """
    digest = hashlib.sha256()
    for key in keys:
        raw = key.encode("utf-8")
        digest.update(len(raw).to_bytes(8, "big"))
        digest.update(raw)
    return (int.from_bytes(digest.digest()[:8], "big") >> 11) / float(1 << 53)


def _policy_number(value: Any) -> float | None:
    """A real finite number, or None. ``bool`` is not a number (it is an ``int``
    subclass in Python, and a guard must never compare True against 1).

    A Python ``int`` too large for a float (JSON has no bound; JavaScript parses
    the same literal to ``Infinity``) is not comparable either — ``float()``
    raises ``OverflowError`` and the value is reported as non-numeric rather than
    escaping as an exception.
    """
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return None
    try:
        number = float(value)
    except (OverflowError, ValueError):  # int wider than a float64 — not comparable.
        return None
    return number if math.isfinite(number) else None


def _policy_scalar_kind(value: Any) -> str | None:
    """The scalar kind of ``value`` — ``"string"``, ``"boolean"``, ``"number"``
    — or None when it is not a comparable scalar at all.

    ``int`` and ``float`` are ONE kind; a ``bool`` is NEVER a number; lists,
    dicts, None, NaN/Inf and out-of-range ints are not scalars. Mirrors the
    TypeScript ``sameScalarKind``.
    """
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, str):
        return "string"
    if _policy_number(value) is not None:
        return "number"
    return None


def _policy_same_scalar_kind(left: Any, right: Any) -> bool:
    """True only when both values are comparable scalars of the same kind."""
    kind = _policy_scalar_kind(left)
    return kind is not None and kind == _policy_scalar_kind(right)


def _policy_equal(left: Any, right: Any) -> bool:
    """Same-kind scalar equality: 1 == 1.0, and nothing else is ever coerced.

    A type mismatch is False in BOTH directions (see ``ne`` in
    :func:`_policy_guard_passes`); lists, dicts and None never equal anything.
    Mirrors the TypeScript ``scalarEquals``.
    """
    if not _policy_same_scalar_kind(left, right):
        return False
    if isinstance(left, bool) or isinstance(left, str):
        return left == right
    return _policy_number(left) == _policy_number(right)


def _policy_non_empty_str(value: Any) -> bool:
    return isinstance(value, str) and value.strip() != ""


def _policy_guard_passes(condition: Any, context: Any) -> bool:
    """Evaluate one ``applies_when`` condition. Anything unclear is False.

    Unknown op, absent context field, type mismatch, or an ``in`` whose value is
    not a list all fail the condition — a guard never guesses its way to true.
    A guard never fails OPEN either: ``ne`` on a type mismatch is False, not
    True. The shared ``guard_cases`` in
    ``public/sdk/parity/runtime-policy.fixtures.json`` are the authority.
    """
    if not isinstance(condition, dict):
        return False
    field_name = condition.get("field")
    op = condition.get("op")
    if not isinstance(field_name, str) or field_name == "" or not isinstance(op, str):
        return False
    if not isinstance(context, dict) or field_name not in context:
        return False
    actual = context[field_name]
    expected = condition.get("value")
    if op == "eq":
        return _policy_equal(actual, expected)
    if op == "ne":
        # Same-kind operands only: a mismatch is "not comparable", never "different".
        return _policy_same_scalar_kind(actual, expected) and not _policy_equal(actual, expected)
    if op in ("gt", "gte", "lt", "lte"):
        left = _policy_number(actual)
        right = _policy_number(expected)
        if left is None or right is None:
            return False
        if op == "gt":
            return left > right
        if op == "gte":
            return left >= right
        if op == "lt":
            return left < right
        return left <= right
    if op == "in":
        if not isinstance(expected, list):
            return False
        return any(_policy_equal(actual, item) for item in expected)
    return False


def _policy_is_structural(policy: Any) -> bool:
    """A document the client is willing to act on at all.

    Missing id / task_family / execute.workflow, or an ``applies_when`` that is
    not a list of objects, makes the document unusable — it is skipped rather
    than repaired.
    """
    if not isinstance(policy, dict):
        return False
    if not _policy_non_empty_str(policy.get("id")) or not _policy_non_empty_str(policy.get("task_family")):
        return False
    execute = policy.get("execute")
    if not isinstance(execute, dict) or not _policy_non_empty_str(execute.get("workflow")):
        return False
    guards = policy.get("applies_when")
    if guards is not None:
        if not isinstance(guards, list) or any(not isinstance(entry, dict) for entry in guards):
            return False
    return True


def _policy_opaque_payload(policy: dict[str, Any]) -> tuple[dict[str, Any] | None, list[str] | None, list[dict[str, Any]] | None]:
    """The policy's ``budget`` / ``verify`` / ``escalation`` for the caller.

    Opaque pass-through: the SDK hands these to the caller and never acts on
    them. A wrong-shaped container becomes None, and list entries are filtered
    to well-typed members (``verify`` to strings, ``escalation`` to objects) —
    mirroring the TS SDK, never repairing a value.
    """
    budget = policy.get("budget")
    verify = policy.get("verify")
    escalation = policy.get("escalation")
    return (
        budget if isinstance(budget, dict) else None,
        [entry for entry in verify if isinstance(entry, str)] if isinstance(verify, list) else None,
        [entry for entry in escalation if isinstance(entry, dict)] if isinstance(escalation, list) else None,
    )


def _policy_fallback_workflow(policy: dict[str, Any]) -> str | None:
    """The declared fallback workflow, or None for the customer's own baseline."""
    fallback = policy.get("fallback")
    if isinstance(fallback, dict) and _policy_non_empty_str(fallback.get("workflow")):
        return str(fallback["workflow"])
    return None


def _policy_int(value: Any) -> int | float | None:
    """A bundle counter (``policy_version`` / ``sequence``), or None.

    JSON has one number type, so a publisher may emit ``7`` or ``7.0`` for the
    same counter. Require a non-negative integer inside JavaScript's safe range
    so Python and TypeScript order every bundle identically. Booleans are
    excluded — ``True`` is not version 1.
    """
    number = _policy_number(value)
    if number is None or not number.is_integer() or number < 0 or number > 9_007_199_254_740_991:
        return None
    return value if isinstance(value, int) else number


def _policy_span_sink(trace: Any) -> Any:
    """Resolve a decision-span sink from whatever the caller passed.

    An :class:`OTelExporter` is used directly. A :class:`Trace` is resolved to a
    trace-bound exporter memoized on that trace, so repeated decisions in one
    trace batch into a single caller-reachable buffer instead of minting one
    exporter per call.
    Anything else (or nothing) means no emission.
    """
    if trace is None:
        return None
    if hasattr(trace, "record_span"):
        return trace
    factory = getattr(trace, "exporter", None)
    if not callable(factory):
        return None
    sink = getattr(trace, "_policy_span_exporter", None)
    if sink is None:
        sink = factory()
        try:
            setattr(trace, "_policy_span_exporter", sink)
        except Exception:  # noqa: BLE001 — observability must never break a decision.
            pass
    return sink


@dataclass(frozen=True)
class PolicyDecision:
    """What the agent should run for one task, decided locally.

    ``decision`` is ``"execute"`` (the policy's execute workflow), ``"fallback"``
    (the policy's declared fallback workflow, which may be None), or
    ``"baseline"`` (the caller's own path; ``workflow`` is always None).
    ``reason`` names why — decisions are observability, they measure nothing and
    claim nothing.
    """

    decision: str
    reason: str
    signed: bool = False
    policy_id: str | None = None
    workflow: str | None = None
    experiment_id: str | None = None
    arm: str | None = None
    propensity: float | None = None
    budget: dict[str, Any] | None = None
    verify: list[Any] | None = None
    escalation: list[Any] | None = None
    policy_version: int | float | None = None
    sequence: int | float | None = None


@dataclass(frozen=True)
class RuntimePolicyState:
    """A snapshot of what the client currently holds."""

    has_bundle: bool
    signed: bool
    kill: bool
    killed_locally: bool
    policy_version: int | float | None = None
    sequence: int | float | None = None


@dataclass(frozen=True)
class RuntimePolicyRefresh:
    """The outcome of one :meth:`RuntimePolicyClient.refresh`."""

    ok: bool
    signed: bool
    error: str | None = None


class RuntimePolicyClient:
    """Local runtime-policy client: fetch rarely, decide locally, never block.

    :meth:`refresh` pulls the project's published policy bundle over the
    gateway's ``GET /sdk/v1/runtime-policy``; :meth:`decide` answers from the
    cached bundle with no network call at all, so a policy outage can never
    stall an agent — it only means ``baseline``.

    Fail-closed throughout: a malformed policy is skipped rather than guessed, a
    missing unit key never invents an arm, and the holdout slice is carved first
    and forced onto the fallback path (holdout SUPPRESSES; it is never the
    riskier variant). Nothing here measures or claims a saving.

    Mirrors the TypeScript ``RuntimePolicyClient``.
    """

    def __init__(
        self,
        cave: Cave,
        *,
        public_key: str | None = None,
        auto_refresh_seconds: float | None = None,
        kill_env: str = _POLICY_KILL_ENV,
        workflow: str | None = None,
    ) -> None:
        self._cave = cave
        self._workflow = workflow or cave.default_workflow
        self._kill_env = kill_env
        self._lock = threading.Lock()
        self._bundle: dict[str, Any] | None = None
        self._signed = False
        self._killed_locally = False
        # A pinned key makes a signature mandatory. An unparseable pin is a
        # caller bug, not a runtime condition: it raises here rather than
        # returning a client that silently rejects every bundle forever
        # (mirrors the TypeScript constructor, which throws).
        self._pinned_key: bytes | None = None
        if public_key is not None:
            decoded = self._decode_key(public_key)
            if decoded is None:
                raise ValueError("runtime_policy public_key must be a base64-encoded 32-byte Ed25519 key")
            self._pinned_key = decoded
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        interval = _policy_number(auto_refresh_seconds)
        if interval is not None and interval > 0:
            self._thread = threading.Thread(
                target=self._auto_refresh,
                args=(interval,),
                name="caveman-runtime-policy",
                daemon=True,
            )
            self._thread.start()

    # ── lifecycle ────────────────────────────────────────────────────────────

    def kill(self) -> None:
        """Latch this client onto the baseline path immediately and locally.

        Takes effect on the very next :meth:`decide`; no network round-trip and
        no way for a later refresh to undo it.
        """
        with self._lock:
            self._killed_locally = True

    def close(self) -> None:
        """Stop the background refresh thread, if one was started."""
        self._stop.set()
        thread = self._thread
        if thread is not None and thread.is_alive():
            thread.join(timeout=1.0)

    def state(self) -> RuntimePolicyState:
        """What the client holds right now."""
        with self._lock:
            bundle = self._bundle
            return RuntimePolicyState(
                has_bundle=bundle is not None,
                signed=self._signed,
                kill=bool(bundle.get("kill")) if isinstance(bundle, dict) else False,
                killed_locally=self._killed_locally,
                policy_version=_policy_int(bundle.get("policy_version")) if isinstance(bundle, dict) else None,
                sequence=_policy_int(bundle.get("sequence")) if isinstance(bundle, dict) else None,
            )

    # ── refresh ──────────────────────────────────────────────────────────────

    def refresh(self) -> RuntimePolicyRefresh:
        """Fetch the current bundle. Never raises; keeps last-known-good.

        A transport failure, a bad signature, an unknown schema version, or a
        bundle older than the cached one all leave the previously accepted
        bundle in place and report ``ok=False``.
        """
        request_headers = headers(self._cave, self._workflow)
        request_headers.pop("content-type", None)  # GET carries no body.
        req = urllib.request.Request(
            f"{self._cave.base_url}{_POLICY_PATH}",
            headers=request_headers,
            method="GET",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                raw = response.read(_POLICY_MAX_RESPONSE_BYTES + 1)
        except Exception:  # noqa: BLE001 — the agent's path must not depend on this call.
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="transport")
        if len(raw) > _POLICY_MAX_RESPONSE_BYTES:
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="oversized_response")
        try:
            payload = json.loads(raw)
        except Exception:  # noqa: BLE001
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="malformed_response")
        return self._accept(payload)

    def _accept(self, payload: Any) -> RuntimePolicyRefresh:
        if not isinstance(payload, dict) or not isinstance(payload.get("bundle"), str):
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="malformed_response")
        # The bundle travels as a STRING: the signature covers these exact bytes,
        # so verification never depends on JSON canonicalization agreeing across
        # three languages. Verify BEFORE parsing.
        body = payload["bundle"].encode("utf-8")
        signature = payload.get("signature")
        published_key = payload.get("public_key")
        signature_present = signature is not None
        public_key_present = published_key is not None

        sig_bytes = None
        if isinstance(signature, dict) and isinstance(signature.get("sig"), str):
            sig_bytes = self._decode_b64(signature["sig"])

        with self._lock:
            pinned = self._pinned_key

        if pinned is not None:
            if sig_bytes is None:
                return RuntimePolicyRefresh(ok=False, signed=self._signed, error="unsigned_rejected")
            if not _ed25519_verify(sig_bytes, body, pinned):
                return RuntimePolicyRefresh(ok=False, signed=self._signed, error="signature_invalid")
            signed = True
        elif sig_bytes is not None:
            # Nothing pinned yet and the server signed: verify with the embedded
            # key and trust it on first use, so a later unsigned or differently
            # keyed bundle cannot downgrade this client.
            embedded = None
            if isinstance(published_key, dict) and isinstance(published_key.get("key"), str):
                embedded = self._decode_key(published_key["key"])
            if embedded is None or not _ed25519_verify(sig_bytes, body, embedded):
                return RuntimePolicyRefresh(ok=False, signed=self._signed, error="signature_invalid")
            signed = True
            # Pin the MOMENT the signature verifies — before the schema/sequence
            # checks below. A validly signed bundle this client then rejects for
            # its contents has still proven which key the server signs with; not
            # pinning here would leave the client downgradeable to unsigned.
            with self._lock:
                if self._pinned_key is None:
                    self._pinned_key = embedded
        elif signature_present or public_key_present:
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="signature_invalid")
        else:
            signed = False

        try:
            bundle = json.loads(payload["bundle"])
        except Exception:  # noqa: BLE001
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="invalid_bundle")
        if not isinstance(bundle, dict):
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="invalid_bundle")
        if bundle.get("schema_version") != _POLICY_SCHEMA_VERSION:
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="unknown_schema_version")

        incoming_version = _policy_int(bundle.get("policy_version"))
        incoming_seq = _policy_int(bundle.get("sequence"))
        if incoming_version is None or incoming_seq is None:
            return RuntimePolicyRefresh(ok=False, signed=self._signed, error="invalid_bundle_counter")

        with self._lock:
            cached = self._bundle
            if isinstance(cached, dict):
                cached_seq = _policy_int(cached.get("sequence"))
                if cached_seq is not None and incoming_seq < cached_seq:
                    return RuntimePolicyRefresh(ok=False, signed=self._signed, error="stale_sequence")
            self._bundle = bundle
            self._signed = signed
        return RuntimePolicyRefresh(ok=True, signed=signed)

    def _auto_refresh(self, interval: float) -> None:
        while not self._stop.wait(interval):
            self.refresh()  # never raises; failures land in state()

    @staticmethod
    def _decode_b64(value: str) -> bytes | None:
        try:
            return base64.b64decode(value, validate=True)
        except Exception:  # noqa: BLE001
            return None

    @classmethod
    def _decode_key(cls, value: Any) -> bytes | None:
        if not isinstance(value, str):
            return None
        decoded = cls._decode_b64(value.strip())
        return decoded if decoded is not None and len(decoded) == 32 else None

    # ── decide ───────────────────────────────────────────────────────────────

    def decide(
        self,
        task_family: str,
        unit_key: str | None = None,
        context: dict[str, Any] | None = None,
        trace: Any = None,
    ) -> PolicyDecision:
        """Decide what to run for ``task_family``. Synchronous, local, total.

        Performs no network I/O and never raises — a garbage context, a broken
        policy, or no bundle at all all resolve to an honest baseline/fallback
        with a reason. ``unit_key`` is the stable identity an experiment is
        assigned on (a task id, a case id — never a random value per call).

        Passing ``trace`` (a :class:`Trace` or an :class:`OTelExporter`) buffers
        one ``caveman.policy.decision`` span through the SDK's existing exporter;
        it opens no new network path and the call stays synchronous either way.
        """
        try:
            decision = self._decide(task_family, unit_key, context)
        except Exception:  # noqa: BLE001 — decide() is total: a bug here must not break the agent.
            decision = PolicyDecision(decision="baseline", reason="policy_unavailable", signed=self._signed)
        try:
            self._emit_span(decision, trace)
        except Exception:  # noqa: BLE001 — observability never breaks a decision.
            pass
        return decision

    def _kill_env_set(self) -> bool:
        """True when the kill environment variable is set to anything but an
        explicit off value. A brake must be easy to pull, so an unrecognized
        value counts as ON; ``0``/``false``/``no``/``off``/empty do not."""
        raw = os.environ.get(self._kill_env)
        if raw is None:
            return False
        return raw.strip().lower() not in ("", "0", "false", "no", "off")

    def _decide(self, task_family: Any, unit_key: Any, context: Any) -> PolicyDecision:
        with self._lock:
            bundle = self._bundle
            signed = self._signed
            killed_locally = self._killed_locally

        # 1. Local brake wins over everything, including a fresh bundle.
        if killed_locally or self._kill_env_set():
            return PolicyDecision(decision="baseline", reason="local_kill", signed=signed)
        # 2. No bundle: the customer's own path, honestly labeled.
        if not isinstance(bundle, dict):
            return PolicyDecision(decision="baseline", reason="policy_unavailable", signed=signed)

        policy_version = _policy_int(bundle.get("policy_version"))
        sequence = _policy_int(bundle.get("sequence"))

        def baseline(reason: str) -> PolicyDecision:
            return PolicyDecision(
                decision="baseline",
                reason=reason,
                signed=signed,
                policy_version=policy_version,
                sequence=sequence,
            )

        # 3. Server-side brake (a pass-through project ships kill=true).
        if bundle.get("kill"):
            return baseline("kill")

        # 4. Candidates for this family.
        raw_policies = bundle.get("runtime_policies")
        policies = raw_policies if isinstance(raw_policies, list) else []
        candidates = [
            entry
            for entry in policies
            if isinstance(entry, dict) and isinstance(entry.get("task_family"), str) and entry["task_family"] == task_family
        ]
        if not candidates:
            return baseline("no_policy")
        usable = [entry for entry in candidates if _policy_is_structural(entry)]
        if not usable:
            return baseline("invalid_policy")
        # 5. Disabled documents are skipped, not repaired.
        enabled = [entry for entry in usable if not entry.get("disabled")]
        if not enabled:
            return baseline("disabled")

        # 6. Guards are AND-ed and fail closed.
        passing = [entry for entry in enabled if all(_policy_guard_passes(c, context) for c in (entry.get("applies_when") or []))]
        if not passing:
            blocked = enabled[0]
            blocked_budget, blocked_verify, blocked_escalation = _policy_opaque_payload(blocked)
            return PolicyDecision(
                decision="fallback",
                reason="guards_failed",
                signed=signed,
                policy_id=str(blocked["id"]),
                workflow=_policy_fallback_workflow(blocked),
                # The declining policy's own budget/verify/escalation ride along:
                # the caller is running its fallback and still needs its terms
                # (mirrors the TypeScript forDoc, which attaches them on every
                # policy-derived decision, guards_failed included).
                budget=blocked_budget,
                verify=blocked_verify,
                escalation=blocked_escalation,
                policy_version=policy_version,
                sequence=sequence,
            )
        # 7. Ambiguity is never resolved by picking one — mirrors the server's
        #    ">1 at the winning tier ⇒ none".
        if len(passing) > 1:
            return baseline("ambiguous_policy")

        policy = passing[0]
        policy_id = str(policy["id"])
        execute_workflow = str(policy["execute"]["workflow"])
        fallback_workflow = _policy_fallback_workflow(policy)
        budget, verify, escalation = _policy_opaque_payload(policy)

        def applied(
            decision: str,
            reason: str,
            workflow: str | None,
            *,
            experiment_id: str | None = None,
            arm: str | None = None,
            propensity: float | None = None,
        ) -> PolicyDecision:
            return PolicyDecision(
                decision=decision,
                reason=reason,
                signed=signed,
                policy_id=policy_id,
                workflow=workflow,
                experiment_id=experiment_id,
                arm=arm,
                propensity=propensity,
                budget=budget,
                verify=verify,
                escalation=escalation,
                policy_version=policy_version,
                sequence=sequence,
            )

        # 8. No experiment: the policy applies to everything past the guards.
        experiment = policy.get("experiment")
        if experiment is None:
            return applied("execute", "applied", execute_workflow)

        # 9. Experiment assignment. The unit key is checked BEFORE the
        #    experiment's shape (mirrors the TypeScript order): a caller with no
        #    stable unit could not be assigned even by a perfect experiment, so
        #    "no_unit_key" is the more actionable reason of the two.
        if not isinstance(unit_key, str) or unit_key == "":
            return applied("fallback", "no_unit_key", fallback_workflow)
        if not isinstance(experiment, dict):
            return applied("fallback", "invalid_experiment", fallback_workflow)
        arms = self._valid_arms(experiment)
        holdout = _policy_number(experiment.get("holdout_frac")) if experiment.get("holdout_frac") is not None else 0.0
        experiment_id = experiment.get("id")
        if arms is None or holdout is None or holdout < 0 or holdout >= 1 or not _policy_non_empty_str(experiment_id):
            return applied("fallback", "invalid_experiment", fallback_workflow)
        experiment_id = str(experiment_id)

        project_id = bundle.get("project_id")
        unit_fraction = policy_unit_fraction(project_id if isinstance(project_id, str) else "", experiment_id, unit_key)
        # The holdout slice is carved FIRST and pushed onto the fallback path:
        # holdout means suppressed, never a riskier variant.
        if unit_fraction < holdout:
            return applied(
                "fallback",
                "holdout",
                fallback_workflow,
                experiment_id=experiment_id,
                arm="holdout",
                propensity=holdout,
            )

        total = sum(weight for _name, weight in arms)
        rescaled = (unit_fraction - holdout) / (1 - holdout)
        cumulative = 0.0
        chosen_name, chosen_weight = arms[-1]  # the last arm absorbs any float shortfall
        for name, weight in arms[:-1]:
            cumulative += weight / total
            if rescaled < cumulative:
                chosen_name, chosen_weight = name, weight
                break
        return applied(
            "execute",
            "applied",
            execute_workflow,
            experiment_id=experiment_id,
            arm=chosen_name,
            # Association is load-bearing: (1-h) * (w/t), NOT ((1-h)*w)/t. The
            # two differ in the last ULP on ~25% of realistic inputs and the
            # parity assignment_vectors assert exact float equality.
            propensity=(1 - holdout) * (chosen_weight / total),
        )

    @staticmethod
    def _valid_arms(experiment: dict[str, Any]) -> list[tuple[str, float]] | None:
        """The experiment's arms, or None when the config cannot be trusted.

        An arm named ``holdout`` is a config error: the holdout slice is minted
        by ``holdout_frac`` alone and must never also be a declared variant.
        """
        raw = experiment.get("arms")
        if not isinstance(raw, list) or not raw:
            return None
        arms: list[tuple[str, float]] = []
        for entry in raw:
            if not isinstance(entry, dict):
                return None
            name = entry.get("name")
            weight = _policy_number(entry.get("fraction"))
            if not _policy_non_empty_str(name) or str(name) == "holdout" or weight is None or weight <= 0:
                return None
            arms.append((str(name), weight))
        return arms

    # ── decision span ────────────────────────────────────────────────────────

    def _emit_span(self, decision: PolicyDecision, trace: Any) -> None:
        sink = _policy_span_sink(trace)
        if sink is None:
            return
        attributes: dict[str, Any] = {
            "cave.policy.decision": decision.decision,
            "cave.policy.reason": decision.reason,
            "cave.policy.signed": decision.signed,
        }
        if decision.policy_id is not None:
            attributes["cave.policy.id"] = decision.policy_id
        if decision.policy_version is not None:
            attributes["cave.policy.version"] = decision.policy_version
        if decision.experiment_id is not None:
            attributes["cave.experiment.id"] = decision.experiment_id
        if decision.arm is not None:
            attributes["cave.experiment.arm"] = decision.arm
        if decision.propensity is not None:
            attributes["cave.experiment.propensity"] = decision.propensity
        parent_span_id = getattr(trace, "span_id", "")
        sink.record_span(
            "caveman.policy.decision",
            parent_span_id=parent_span_id if isinstance(parent_span_id, str) else "",
            kind=1,  # SPAN_KIND_INTERNAL: a local decision, not a call out.
            workflow=self._workflow,
            attributes=attributes,
        )
