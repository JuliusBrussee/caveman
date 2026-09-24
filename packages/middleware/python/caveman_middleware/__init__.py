"""Optional native adapters. Import only the framework module your app uses."""
__version__ = "0.1.0a1"

# Stdlib and SDK only: no framework is imported here.
from ._versions import COMPATIBILITY, preflight, ready  # noqa: E402

__all__ = ["COMPATIBILITY", "preflight", "ready"]
