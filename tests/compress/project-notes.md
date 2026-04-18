# Project Notes — Taskflow

## Architecture Decision: Background Job Processing (March 2026)

Team pick BullMQ for background jobs. No custom build, no AWS SQS. Why:

Already run Redis for cache+sessions. BullMQ need Redis. No new infra. SQS break local dev, hurt contributor setup.

BullMQ have retries, exponential backoff, priority queues, rate limiting, scheduling — no need build own. Active maintenance, good TypeScript support.

First job types: email notifications, file uploads (thumbnails, virus scan), third-party sync (Slack, GitHub), expired session cleanup. More later.

Ops concern: monitoring complexity. Fix: BullMQ dashboard added as admin route at `/admin/jobs`. See queue health, failed jobs, rates. No extra tool needed.

## Performance Investigation: Dashboard Slowness (March 2026)

Sam investigate: dashboard unusable at 500+ tasks. Findings:

Main bottleneck: N+1 query. Load all tasks, then per-task query for assignee profile. 500 tasks = 501 queries. Slow.

Frontend issue: task list render all at once, no virtualization. React struggle with 500+ task cards each with children, tooltips, dropdowns.

Proposed solutions:
1. Add JOIN to load assignees in one query
2. Add composite index on `tasks(project_id, status, updated_at)`
3. Cursor-based pagination on API (50 tasks/page)
4. Virtual scrolling via TanStack Virtual
5. Redis cache dashboard response, short TTL (30s), invalidate on change

Do 1, 2, 3 first — fix root cause. 4 and 5 later if needed.

## Meeting Notes: Security Review (February 2026)

External audit find issues:

Critical: SQL injection in task search + user-by-email endpoints. Cause: string interpolation in queries. Fixed: switched to Knex.js parameterized queries everywhere. Added ESLint rule to catch raw string concat in query builders.

JWT expiry too long (30 days). Reduced: access token 15min, refresh token 7 days. Refresh token in HttpOnly cookie, access token in memory only — never `localStorage`.

Missing Content Security Policy headers. Added to Next.js middleware. Currently report-only mode. Switch to enforcement after 2 weeks monitoring.

Rate limiting missing from public API. Alex add `express-rate-limit` with Redis store — share state across API instances.

## Design Decision: Component Library (January 2026)

Maya lead eval. Options:

1. **shadcn/ui with Radix primitives** — copy components into project, Radix for a11y, Tailwind for styles. Pros: full code ownership, easy customize, great a11y. Cons: more setup, self-maintain.

2. **Material UI (MUI)** — most popular React lib. Pros: mature, docs, big community. Cons: large bundle, opinionated, hard customize, vendor lock-in.

3. **Chakra UI** — prop-based styling. Pros: good DX, accessible. Cons: runtime CSS-in-JS slow, smaller ecosystem.

Pick option 1 (shadcn/ui + Radix). Max control, Radix a11y, Tailwind match existing strategy, small bundle. Tradeoff: self-maintain. Team OK with that.

## Technical Debt Inventory (January 2026)

Auth system: rushed at launch, messy. Token refresh split across 3 files, inconsistent error handling. WebSocket auth separate from HTTP auth — causing reconnect race condition now. Needs refactor, but team wait for better test coverage first.

Test suite: inconsistent. Mix of old Enzyme and Testing Library. Mocking varies: `jest.mock`, manual mocks, MSW. Standardize on Testing Library + MSW, migrate Enzyme tests.

DB migrations: early ones mix schema changes + data transforms. Slow, hard to rollback. Rule going forward: schema-only migrations, data transforms in separate scripts.

Frontend build: migrated Webpack → Vite, fixed slow builds. Leftover Webpack configs and polyfills still present, need cleanup.