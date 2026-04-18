# User Preferences

## Code Style

Prefer TypeScript strict mode always. No `any` unless unavoidable — comment why if used. Proper types catch bugs early.

React: functional components + hooks only. No class components. State local as possible; lift only when truly needed. Rather extra re-renders than complex global state.

Imports: organized, external/internal/relative separated. Use path aliases (`@/components/...`) not deep relative paths.

## Testing Approach

Always write tests for new functionality. Test behavior from user perspective, not implementation. "Clicking submit creates task" not "handleSubmit calls createTask."

React: Testing Library, no internal state/lifecycle testing. API endpoints: integration tests hit real DB — mocked tests passed but prod broke too many times.

No 100% coverage needed. Critical paths matter. Services: thorough unit tests. UI: happy path + key error states.

## Communication Style

Senior engineer, 2 years on project. Skip basic concepts. Concise, direct. Tradeoffs: options + pros/cons, no paragraphs.

Show actual code, not descriptions. Multiple files: show all at once.

Comments explain "why" not "what." Code needing "what" comments needs refactoring instead.

## Workflow Preferences

Read existing code before changes. Follow existing conventions over preferred approach — consistency > preference in team codebase.

PRs: small, focused. 3 small > 1 large. Each PR does one thing. Found something else? Separate PR.

Run linter + type checker before committing. Run manually after large refactors even with pre-commit hooks.

## Things to Avoid

No `console.log` — use `src/lib/logger.ts`. Logs reach prod and clutter output.

No new deps without discussion. Keep bundle small, avoid duplicate libraries. Prefer built-in Node/browser APIs over packages.

No single-consumer abstractions. Premature abstraction worse than duplication. Wait for 3+ use cases before extracting shared utility.

Never commit `.env` or secrets/keys/credentials. `.env.example` gets placeholder values only.