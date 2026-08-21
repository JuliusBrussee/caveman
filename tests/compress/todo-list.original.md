# Sprint 24 — Task List

## High Priority

- [ ] **TF-456: Fix WebSocket reconnection race condition** — The real-time collaboration feature fails to reconnect after network interruption because the WebSocket reconnection logic races with the JWT refresh flow. The client tries to reconnect with an expired token before the refresh completes. Assigned to Alex Chen. Due by April 11, 2026. This is blocking the enterprise demo scheduled for April 14.

- [ ] **TF-489: Optimize dashboard query for large projects** — The main dashboard takes over 8 seconds to load when a project has more than 500 tasks. Sam has identified that the issue is a missing composite index on `tasks(project_id, status, updated_at)` combined with an N+1 query in the task assignee resolution. Assigned to Sam Rivera. Due by April 9, 2026.

- [ ] **TF-501: Implement chunked file upload** — Large attachments over 10MB timeout on slower connections. We need to refactor the upload endpoint to support multipart chunked uploads with resume capability. The frontend should show upload progress and allow cancellation. Assigned to Jordan Kim. Due by April 15, 2026.

## Medium Priority

- [ ] **TF-478: Add Slack notification integration** — When a task is assigned or its status changes, send a notification to the configured Slack channel. We've already set up the webhook infrastructure. Jordan needs to wire up the event handlers in the task service and add the Slack message formatting. Assigned to Jordan Kim. Due by April 18, 2026.

- [ ] **TF-492: Fix timezone display for due dates** — Due dates are currently displayed in UTC instead of the user's local timezone. This requires changes in both the API response serialization (add timezone info to the user profile response) and the frontend date formatting utilities. There's a shared `formatDate` helper in `src/lib/dates.ts` that needs to accept a timezone parameter. Assigned to Maya Patel. Due by April 16, 2026.

- [ ] **TF-503: Add keyboard shortcuts for common actions** — Users have requested keyboard shortcuts for creating new tasks (Ctrl+N), searching (Ctrl+K), and navigating between views. We should use a centralized keyboard shortcut manager rather than adding individual event listeners. Consider using the `tinykeys` library which is only 700 bytes gzipped. Assigned to Maya Patel. Due by April 20, 2026.

## Low Priority

- [ ] **TF-467: Update README with new architecture diagram** — The current architecture diagram in the README is outdated and doesn't reflect the recent addition of the background job processing system or the Redis caching layer. Should be updated before the next open-source community call on April 25, 2026. Unassigned.

- [ ] **TF-510: Investigate Playwright test flakiness** — The E2E test for the drag-and-drop task reordering feature fails intermittently in CI (about 1 in 5 runs). It appears to be a timing issue with the animation completion detection. Not blocking anything currently but it's annoying and reduces confidence in the test suite. Unassigned.

- [ ] **TF-498: Clean up deprecated API endpoints** — Several v1 API endpoints were deprecated three months ago and can now be safely removed. The frontend has been updated to use v2 endpoints exclusively. The old endpoints are: `GET /api/v1/tasks`, `POST /api/v1/tasks`, `PUT /api/v1/tasks/:id`. Unassigned.

## Completed This Sprint

- [x] **TF-445: Migrate from Webpack to Vite** — Completed by Maya on April 2. Build time reduced from 45 seconds to 8 seconds. Hot module replacement is significantly faster.
- [x] **TF-451: Add rate limiting to public API endpoints** — Completed by Alex on April 3. Using `express-rate-limit` with Redis store. Limits set to 100 requests per minute for authenticated users, 20 for unauthenticated.
- [x] **TF-460: Fix CORS configuration for staging environment** — Completed by Sam on April 1. The staging domain was missing from the allowed origins list.
