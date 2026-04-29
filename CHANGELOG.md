# Changelog

## Unreleased

- Added mode-tracker support for natural-language activation via `less tokens please` and for the `/caveman:compress` alias.
- Aligned README and related docs with the current command set, activation behavior, and compress workflow.
- Exposed the help surface consistently in `GEMINI.md` and `AGENTS.md`.
- Hardened `verify_repo` coverage to validate mode tracking, reinforcement output, and compress-mode behavior.
- Added unit coverage for `XDG_CONFIG_HOME` default mode resolution in `tests/test_hooks.py`.
