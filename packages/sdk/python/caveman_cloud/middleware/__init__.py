"""Compression-only framework runtime. Optional frameworks import nothing here.

Experimental: this subpackage implements middleware protocol 1.1 and may change in any minor
release of ``caveman-sdk``. The core ``caveman_cloud`` client API is not affected.
"""
from .async_runtime import AsyncMiddlewareRuntime, ensure_async, ensure_sync
from .protocol import (
    CircuitBreaker, classify_failure, manifest_window, normalize_scope, normalize_scope_token, opaque_manifest_value,
    parse_capabilities, plan_budget, resolve_deadlines, resolve_endpoint, resolve_proxy, warn_once,
)
from .runtime import MiddlewareRuntime, RECOVERY_DESCRIPTION, RECOVERY_SCHEMA
from .types import (
    REASON_CATALOG, Adapter, BudgetItem, BudgetResult, CallReport, CapabilitiesView, Candidate, DecisionCounts, DecisionEvent,
    EffectiveLimits, FailureInput, FailureOutcome, MiddlewareError, Optimization, PreflightReport, RecoveryBinding, Scope,
)
from .validate import scope_key, sha256

__all__ = [
    "MiddlewareRuntime", "AsyncMiddlewareRuntime", "ensure_sync", "ensure_async",
    "Adapter", "CallReport", "PreflightReport", "Candidate", "Scope", "RecoveryBinding", "Optimization", "MiddlewareError",
    "DecisionEvent", "DecisionCounts", "CapabilitiesView", "EffectiveLimits", "FailureInput", "FailureOutcome", "BudgetItem",
    "BudgetResult", "REASON_CATALOG",
    "normalize_scope", "normalize_scope_token", "parse_capabilities", "classify_failure", "CircuitBreaker", "resolve_deadlines",
    "plan_budget", "manifest_window", "opaque_manifest_value", "resolve_endpoint", "resolve_proxy", "warn_once",
    "sha256", "scope_key", "RECOVERY_DESCRIPTION", "RECOVERY_SCHEMA",
]
