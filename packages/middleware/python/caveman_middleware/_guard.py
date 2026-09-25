"""The one fail-open guard (Decision 4) and scope resolution every adapter shares.

Guard only Caveman's own preparation, never the native provider/handler call:
a provider exception must still reach the caller unchanged.
"""
from caveman_cloud.middleware import MiddlewareError, Scope, normalize_scope, warn_once


def fail_open(runtime, adapter, error):
    """Non-strict: warn once and return ``adapter_error`` so the caller passes the original input through.

    Strict: raise, keeping an SDK ``MiddlewareError`` as is and wrapping anything else as ``adapter_error``.
    """
    if getattr(runtime, "strict", False):
        if isinstance(error, MiddlewareError):
            raise error
        raise MiddlewareError("adapter_error") from error
    warn_once(adapter, "adapter_error")
    return "adapter_error"


def resolve_scope(runtime, adapter, source, *args):
    """Normalized Scope from a fixed value or a trusted resolver, else None (``invalid_scope``, warned once).

    A resolver that raises (for example a missing ``thread_id``) or returns something unusable never
    breaks the native call in non-strict mode; strict raises ``invalid_scope``. Emails and other free-form
    identifiers are hashed into valid tokens by ``normalize_scope`` (protocol §9).
    """
    try:
        scope = source if isinstance(source, Scope) else source(*args)
        normalized = normalize_scope(scope) if isinstance(scope, Scope) else None
    except Exception as error:
        if getattr(runtime, "strict", False):
            raise MiddlewareError("invalid_scope") from error
        normalized = None
    if normalized is None:
        if getattr(runtime, "strict", False):
            raise MiddlewareError("invalid_scope")
        warn_once(adapter, "invalid_scope")
    return normalized


def recovery(runtime, scope):
    """Wrap-time recovery binding, or None for an unusable scope. Never raises (Decision 3):
    strict mode reports ``invalid_scope`` from the request path instead."""
    try:
        return runtime.recovery(scope)
    except MiddlewareError:
        return None


def recovery_failed(adapter, error):
    """A ``caveman_retrieve`` call the runtime refused (unknown or expired handle, 404/410/503) becomes the
    ``{"error": code}`` tool result the model reads, never an exception that ends the host's run. Warns once.

    Callers catch ``MiddlewareError`` only, so cancellation always propagates.
    """
    warn_once(adapter, error.code)
    return {"error": error.code}


def recovery_name_conflict(runtime, adapter):
    """A host tool already owns ``caveman_retrieve``: recovery stays off for this registration.

    Reported through ``decline()`` so hosts get the ``on_diagnostic`` signal, and strict ``ready()`` raises it.
    """
    warn_once(adapter, "recovery_name_conflict")
    runtime.decline("recovery_name_conflict", adapter)
