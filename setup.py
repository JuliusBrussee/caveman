# caveman-agent — pip packaging shim.
#
# The real installer is bin/install.js (single source of truth per CLAUDE.md);
# this setup.py only teaches setuptools to bundle the JS payload (bin/, src/,
# agents/, skills/) into the wheel as caveman_agent/_payload/ so the pip
# package behaves exactly like a repo clone at runtime — no network fetch, and
# the packaged installer version always matches the repo version.
#
# Everything declarative lives in pyproject.toml; this file exists only for
# the custom build_py step (setuptools offers no other supported way to pull
# trees from outside the package dir into package data).

import os
import shutil

from setuptools import setup
from setuptools.command.build_py import build_py

PAYLOAD_DIRS = ['bin', 'src', 'agents', 'skills']
PAYLOAD_PRUNE = {'node_modules', '__pycache__', '.pytest_cache'}


def _ignore(_dir, names):
    return [n for n in names if n in PAYLOAD_PRUNE or n.endswith('.pyc')]


class BuildWithPayload(build_py):
    def run(self):
        super().run()
        root = os.path.dirname(os.path.abspath(__file__))
        target = os.path.join(self.build_lib, 'caveman_agent', '_payload')
        for d in PAYLOAD_DIRS:
            src = os.path.join(root, d)
            if not os.path.isdir(src):
                raise RuntimeError(
                    f'caveman-agent: payload dir {d}/ missing — building from an '
                    'incomplete source tree? (sdist must graft bin/ src/ agents/ skills/)'
                )
            shutil.copytree(src, os.path.join(target, d), dirs_exist_ok=True, ignore=_ignore)
        # Auto-bootstrap .pth: a file at build_lib root lands in site-packages
        # root, where Python's site module executes its `import` line at every
        # interpreter startup (the editable-install mechanism). This is what
        # makes `pip install caveman-agent` a COMPLETE install — the first
        # Python run after install triggers the (guarded, at-most-once,
        # background) agent setup. See python/caveman_agent/_bootstrap.py for
        # the guards; opt out with CAVEMAN_NO_AUTO_INSTALL=1.
        with open(os.path.join(self.build_lib, 'caveman_agent_bootstrap.pth'), 'w') as f:
            f.write('import caveman_agent._bootstrap\n')


setup(cmdclass={'build_py': BuildWithPayload})
