"""Public native-adapter inputs. Wire keys remain identical across languages."""
from dataclasses import dataclass, field
from types import MappingProxyType
from typing import Any, Callable, Final, Literal, Mapping


@dataclass(frozen=True)
class Scope:
    namespace: str
    session_id: str
    branch_id: str = "main"
    cache_epoch: str = "0"


@dataclass(frozen=True)
class Adapter:
    id: str
    version: str
    framework_version: str
    serialization_revision: str


@dataclass(frozen=True)
class Candidate:
    id: str
    content: str
    source_id: str | None = None
    kind: Literal["tool_result", "artifact"] = "tool_result"
    cache_region: Literal["frozen_prefix", "live_zone", "uncached"] = "live_zone"
    protected: bool = False
    opaque: bool = False


@dataclass(frozen=True, eq=False)
class RecoveryBinding:
    id: str
    scope: Scope
    name: str
    description: str
    input_schema: Mapping[str, Any]
    execute: Callable[..., Any]


@dataclass(frozen=True)
class Optimization:
    status: str
    reason: str
    replacements: list[dict[str, Any]] = field(default_factory=list)
    plan: dict[str, Any] | None = None
    request: dict[str, Any] | None = None
    cache_continuity: str = "unavailable"
    # Protocol 1.1 decision-event inputs (§14); None/0 when optimize() never reached them.
    counts: "DecisionCounts | None" = None
    latency_ms: int = 0
    runtime_build: str | None = None


@dataclass(frozen=True)
class CallReport:
    """Final native-call decision; no source text or provider credentials."""
    schema_version: int
    status: Literal["applied", "reused", "skipped", "recorded", "disabled"]
    reason: str
    transform_ids: tuple[str, ...]
    replacement_count: int
    reused_count: int
    adapter: str | None
    logical_call_id: str | None
    attempt_id: str | None


@dataclass(frozen=True)
class PreflightReport:
    """Startup discovery only; readiness does not establish savings or quality."""
    schema_version: int
    status: Literal["ready", "disabled", "unavailable"]
    reason: str
    configured_mode: str
    runtime_mode: str | None
    runtime_build: str | None
    policy_revision: str | None
    persistent: bool | None
    recovery: bool | None
    action: str


class MiddlewareError(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(f"Caveman middleware: {code}")


# ---- Protocol 1.1 (docs/technical/middleware-protocol.md). Values are pinned by
# packages/sdk/parity/middleware-v1_1.fixtures.json `constants` / `reason_catalog`.
PROTOCOL_RANGE: Final = MappingProxyType({"min": 1, "max": 1})
MIDDLEWARE_FEATURES_HEADER: Final = "Caveman-Middleware-Features"
MIDDLEWARE_CLIENT_HEADER: Final = "Caveman-Middleware-Client"
MIDDLEWARE_CLIENT_PRODUCT: Final = "caveman-sdk-python"
Feature = Literal["http_status_v2", "originals_lifecycle", "revision_tolerant", "tolerant_reader"]
KNOWN_FEATURES: Final[tuple[str, ...]] = ("http_status_v2", "originals_lifecycle", "revision_tolerant", "tolerant_reader")
# Features the client asks the runtime to switch on; sent on every request.
CLIENT_FEATURES: Final[tuple[str, ...]] = ("http_status_v2", "revision_tolerant")
CLIENT_FEATURES_HEADER_VALUE: Final = "http_status_v2, revision_tolerant"
# K5: only replacements whose transform declares one of these recovery kinds apply.
CLIENT_RECOVERY_ALLOWLIST: Final[tuple[str, ...]] = ("exact_ccr",)
RECOVERY_MARKER_PREFIX: Final = "[caveman: shortened; exact original via caveman_retrieve handle="
RECOVERY_HANDLE_PATTERN: Final = r"^cmw_[a-f0-9]{48}$"
REASON_PATTERN: Final = r"^[a-z][a-z0-9_]{0,63}$"
SCOPE_TOKEN_PATTERN: Final = r"^[A-Za-z0-9._:/-]{1,256}$"
SCOPE_HASH_PREFIX: Final = "h-"
SCOPE_HASH_HEX_CHARS: Final = 32
DEFAULT_BRANCH_ID: Final = "main"
DEFAULT_CACHE_EPOCH: Final = "0"
MIDDLEWARE_DEFAULTS: Final = MappingProxyType({
    "bootstrap_deadline_ms": 500, "retrieve_deadline_ms": 5000, "max_concurrency": 16, "max_segments": 256,
    "max_manifest_items": 4096, "manifest_bytes": 2 << 20, "receipt_bytes": 16384, "replaced_memory_entries": 4096,
    "retry_after_cap_ms": 30000, "warn_once_entries": 1024,
})
BreakerState = Literal["closed", "open", "half_open"]


@dataclass(frozen=True)
class BreakerParams:
    consecutive_failures: int = 5
    window_size: int = 20
    window_failures: int = 10
    open_ms: int = 30000


BREAKER_DEFAULTS: Final = BreakerParams()


@dataclass(frozen=True)
class ReasonPolicy:
    """K7. strict: raise = optimize()/adapter call raises in strict mode;
    ready = surfaced only by ready()/preflight(); none = never an error."""
    breaker: bool
    warn_once: bool
    strict: Literal["raise", "ready", "none"]


REASON_CATALOG: Final[Mapping[str, ReasonPolicy]] = MappingProxyType({
    "adapter_error": ReasonPolicy(False, True, "raise"),
    "cache_state_unavailable": ReasonPolicy(False, True, "none"),
    "capacity": ReasonPolicy(False, True, "raise"),
    "circuit_open": ReasonPolicy(False, True, "raise"),
    "closed": ReasonPolicy(False, True, "none"),
    "deadline": ReasonPolicy(True, True, "raise"),
    "deleted": ReasonPolicy(False, True, "raise"),
    "disabled": ReasonPolicy(False, False, "none"),
    "eligible": ReasonPolicy(False, False, "none"),
    "epoch_changed": ReasonPolicy(False, True, "none"),
    "expired": ReasonPolicy(False, False, "none"),
    "forbidden_namespace": ReasonPolicy(False, True, "raise"),
    "forbidden_origin": ReasonPolicy(False, True, "raise"),
    "identity_conflict": ReasonPolicy(False, True, "none"),
    "insecure_transport_not_enabled": ReasonPolicy(False, True, "ready"),
    "invalid_endpoint": ReasonPolicy(False, True, "ready"),
    "invalid_plan": ReasonPolicy(True, True, "raise"),
    "invalid_request": ReasonPolicy(False, True, "raise"),
    "invalid_scope": ReasonPolicy(False, True, "raise"),
    "no_candidate": ReasonPolicy(False, False, "none"),
    "not_found": ReasonPolicy(False, True, "raise"),
    "not_smaller": ReasonPolicy(False, False, "none"),
    "opaque_part": ReasonPolicy(False, False, "none"),
    "payload_budget": ReasonPolicy(False, True, "none"),
    "payload_limit": ReasonPolicy(False, True, "raise"),
    "protected": ReasonPolicy(False, False, "none"),
    "provider_state_retained": ReasonPolicy(False, True, "none"),
    "quota_exceeded": ReasonPolicy(False, True, "raise"),
    "record": ReasonPolicy(False, False, "none"),
    "recovery_name_conflict": ReasonPolicy(False, True, "raise"),
    "recovery_unavailable": ReasonPolicy(False, True, "raise"),
    "recovery_unbound": ReasonPolicy(False, True, "none"),
    "redirect_refused": ReasonPolicy(True, True, "raise"),
    "remote_content_not_enabled": ReasonPolicy(False, True, "ready"),
    "runtime_unavailable": ReasonPolicy(True, True, "raise"),
    "unauthorized": ReasonPolicy(False, True, "raise"),
    "unknown_capability": ReasonPolicy(False, True, "raise"),
    "unsupported_shape": ReasonPolicy(False, True, "raise"),
    "unsupported_version": ReasonPolicy(False, True, "ready"),
    "version_unavailable": ReasonPolicy(False, True, "ready"),
    "version_unverified": ReasonPolicy(False, True, "none"),
})


@dataclass(frozen=True)
class EffectiveLimits:
    deadline_ms: int
    request_bytes: int
    segment_bytes: int
    page_bytes: int
    retrieve_deadline_ms: int
    max_segments: int
    max_manifest_items: int
    receipt_bytes: int
    queue_depth: int | None
    retrieve_queue_depth: int | None
    quota_requests_per_minute: int | None


@dataclass(frozen=True)
class CapabilitiesView:
    """Tolerant parse of a capabilities document (K1/K2)."""
    capabilities: Mapping[str, Any]
    legacy: bool  # True when the document has no `features` array (protocol 1.0 runtime)
    features: tuple[str, ...]  # server features this client understands, sorted
    transforms: tuple[Mapping[str, Any], ...]  # usable: well-formed, deterministic, allowlisted recovery, first per id
    mode: Literal["record", "compress"]  # unrecognized runtime modes read as "record"
    limits: EffectiveLimits
    max_retention_seconds: int | None


@dataclass(frozen=True)
class FailureInput:
    """Client failure classification input (K4/K8); body is the raw response text."""
    transport: Literal["response", "error", "timeout", "redirect"]
    status: int | None = None
    body: str | None = None
    retry_after: str | None = None


@dataclass(frozen=True)
class FailureOutcome:
    reason: str
    breaker: bool
    clear_capabilities: bool
    retry_after_ms: int | None


@dataclass(frozen=True)
class BudgetItem:
    """K10. key = f"{original_sha256}:{segment_id}"; bytes = wire bytes of the segment plus one."""
    id: str
    key: str
    bytes: int


@dataclass(frozen=True)
class BudgetResult:
    """K10 admission: ids in input order."""
    admitted: tuple[str, ...]
    skipped: tuple[str, ...]


@dataclass(frozen=True)
class DecisionCounts:
    candidates: int
    sent: int
    protected: int
    opaque: int
    unsupported: int
    budget_skipped: int
    skipped: int
    replaced: int
    reused: int


@dataclass(frozen=True)
class DecisionEvent:
    """One content-free event per report() (K9); asdict() keys are identical in every SDK."""
    schema_version: int
    status: Literal["applied", "reused", "skipped", "recorded", "disabled"]
    reason: str
    adapter: str | None
    logical_call_id: str | None
    attempt_id: str | None
    transform_ids: tuple[str, ...]
    latency_ms: int
    counts: DecisionCounts
    runtime_build: str | None
    cache_continuity: Literal["persistent_choices", "unavailable", "off"]


# OTel names (K9). Spans are CLIENT kind; durations are seconds.
OTEL: Final = MappingProxyType({
    "spans": MappingProxyType({"optimize": "caveman.middleware.optimize", "retrieve": "caveman.middleware.retrieve",
                               "receipt": "caveman.middleware.receipt"}),
    "decisions_counter": "caveman.middleware.decisions",
    "duration_histogram": "caveman.middleware.duration",
    "attribute_prefix": "caveman.middleware.",
    "usage": MappingProxyType({"input_tokens": "gen_ai.usage.input_tokens", "output_tokens": "gen_ai.usage.output_tokens",
                               "cache_read_tokens": "gen_ai.usage.cache_read.input_tokens",
                               "cache_write_tokens": "gen_ai.usage.cache_creation.input_tokens"}),
})
