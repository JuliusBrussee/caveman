"""Optional native adapters. Import only the framework module your app uses."""
# Stdlib and SDK only: no framework is imported here. __version__ is the installed distribution's.
from ._versions import COMPATIBILITY, VERSION as __version__, preflight, ready

__all__ = ["COMPATIBILITY", "preflight", "ready"]
