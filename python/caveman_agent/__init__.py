"""caveman-agent — pip shim around the caveman unified installer.

The actual logic lives in bin/install.js (Node, bundled into this package as
_payload/). This package exists so Python shops can put caveman in
requirements.txt and bootstrap agents with one idempotent command:

    pip install caveman-agent
    caveman install --non-interactive
"""

__version__ = "1.9.0"
