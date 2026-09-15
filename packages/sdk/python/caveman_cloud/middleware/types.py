"""Public native-adapter inputs. Wire keys remain identical across languages."""
from dataclasses import dataclass, field
from typing import Any, Callable, Literal, Mapping


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


class MiddlewareError(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(f"Caveman middleware: {code}")
