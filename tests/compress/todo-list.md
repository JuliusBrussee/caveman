# Sprint 24 — Task List

## High Priority

- [ ] **TF-456: Fix WebSocket reconnection race condition** — RT collab fail reconnect after network drop. WS reconnect race JWT refresh. Client reconnect w/ expired token before refresh done. Alex Chen. Due Apr 11. Blocks enterprise demo Apr 14.

- [ ] **TF-489: Optimize dashboard query for large projects** — Dashboard 8s+ load when project >500 tasks. Missing composite index on `tasks(project_id, status, updated_at)` + N+1 query in task assignee resolution. Sam Rivera. Due Apr 9.

- [ ] **TF-501: Implement chunked file upload** — Attachments >10MB timeout on slow connections. Refactor upload endpoint: multipart chunked uploads w/ resume. Frontend: show progress, allow cancel. Jordan Kim. Due Apr 15.

## Medium Priority

- [ ] **TF-478: Add Slack notification integration** — Notify Slack channel on task assign/status change. Webhook infra ready. Wire event handlers in task service + Slack msg formatting. Jordan Kim. Due Apr 18.

- [ ] **TF-492: Fix timezone display for due dates** — Dates show UTC not user local tz. Fix API serialization (add tz to user profile response) + frontend date utils. `formatDate` in `src/lib/dates.ts` needs tz param. Maya Patel. Due Apr 16.

- [ ] **TF-503: Add keyboard shortcuts for common actions** — Users want shortcuts: new task (Ctrl+N), search (Ctrl+K), view nav. Use centralized shortcut manager, not individual listeners. Consider `tinykeys` (700b gzipped). Maya Patel. Due Apr 20.

## Low Priority

- [ ] **TF-467: Update README with new architecture diagram** — Diagram outdated, missing background job system + Redis cache layer. Update before open-source community call Apr 25. Unassigned.

- [ ] **TF-510: Investigate Playwright test flakiness** — E2E drag-and-drop reorder fails ~1/5 CI runs. Timing issue w/ animation completion detection. Not blocking, but hurts test confidence. Unassigned.

- [ ] **TF-498: Clean up deprecated API endpoints** — v1 endpoints deprecated 3mo ago, safe to remove. Frontend on v2 exclusively. Remove: `GET /api/v1/tasks`, `POST /api/v1/tasks`, `PUT /api/v1/tasks/:id`. Unassigned.

## Completed This Sprint

- [x] **TF-445: Migrate from Webpack to Vite** — Maya, Apr 2. Build 45s→8s. HMR much faster.
- [x] **TF-451: Add rate limiting to public API endpoints** — Alex, Apr 3. `express-rate-limit` w/ Redis. 100 req/min authed, 20 unauthed.
- [x] **TF-460: Fix CORS configuration for staging environment** — Sam, Apr 1. Staging domain missing from allowed origins.