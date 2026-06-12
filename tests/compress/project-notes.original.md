# Project Notes — Taskflow

## Architecture Decision: Background Job Processing (March 2026)

After extensive discussion, the team decided to adopt BullMQ for background job processing instead of building a custom solution or using AWS SQS. The primary reasons for this decision were:

The team is already familiar with Redis, which is a requirement for BullMQ, and we're already running Redis for caching and session storage. Adding BullMQ doesn't introduce any new infrastructure dependencies. The alternative of using AWS SQS would have required significant changes to our local development setup and would have made it harder for contributors to run the full stack locally.

BullMQ provides built-in support for job retries with exponential backoff, priority queues, rate limiting, and job scheduling — all features we would have had to build ourselves with a custom solution. The library is actively maintained and has good TypeScript support.

The initial use cases for background jobs are: sending email notifications, processing file uploads (generating thumbnails, virus scanning), syncing data with third-party integrations (Slack, GitHub), and cleaning up expired sessions. We expect to add more job types as the application grows.

One concern raised during the discussion was the operational complexity of monitoring background jobs. To address this, we added the BullMQ dashboard as an admin-only route at `/admin/jobs`. This provides visibility into queue health, failed jobs, and processing rates without requiring a separate monitoring tool.

## Performance Investigation: Dashboard Slowness (March 2026)

Sam spent a week investigating why the main dashboard becomes unusable for projects with more than 500 tasks. Here are the findings:

The primary bottleneck is the database query that loads the task list. The current implementation fetches all tasks for a project in a single query, then for each task, makes a separate query to load the assignee's profile. This classic N+1 problem means that loading 500 tasks results in 501 database queries. With network latency to the database, this adds up to several seconds.

The secondary issue is on the frontend. The task list component renders all tasks at once without any form of virtualization. React's reconciliation algorithm struggles with a DOM tree containing 500+ task cards, each with multiple child elements, tooltips, and dropdown menus.

Proposed solutions:
1. Add a JOIN to the task query to load assignees in a single query instead of N+1
2. Add a composite index on `tasks(project_id, status, updated_at)` for the default sort order
3. Implement cursor-based pagination on the API (load 50 tasks at a time)
4. Add virtual scrolling on the frontend using TanStack Virtual
5. Cache the dashboard response in Redis with a short TTL (30 seconds) and invalidate on task changes

We decided to implement solutions 1, 2, and 3 first, as they address the root cause. Solutions 4 and 5 will be added later if the first three aren't sufficient.

## Meeting Notes: Security Review (February 2026)

The security audit conducted by an external firm identified several areas for improvement:

The most critical finding was that our SQL queries in several older endpoints were using string interpolation instead of parameterized queries. This created SQL injection vulnerabilities in the task search endpoint and the user lookup by email endpoint. These have since been fixed by switching to Knex.js parameterized queries throughout the codebase. We also added an ESLint rule to flag raw string concatenation in query builder calls.

The audit also found that our JWT tokens had an excessively long expiration time of 30 days. We reduced this to 15 minutes for access tokens and introduced a separate refresh token with a 7-day expiration. The refresh token is stored in an HttpOnly cookie and the access token is kept in memory only, never in localStorage.

Another recommendation was to implement Content Security Policy headers, which we have added to the Next.js middleware. The CSP is currently in report-only mode while we verify that it doesn't break any legitimate functionality. We plan to switch to enforcement mode after two weeks of monitoring.

Rate limiting was also flagged as missing from our public API endpoints. Alex implemented this using express-rate-limit with a Redis store, allowing rate limit state to be shared across multiple API server instances.

## Design Decision: Component Library (January 2026)

Maya led the evaluation of component libraries for the frontend redesign. The options considered were:

1. **shadcn/ui with Radix primitives** — Not a traditional component library, but a collection of beautifully designed, accessible components that you copy into your project. Built on Radix UI primitives for accessibility, styled with Tailwind CSS. Pros: full ownership of the code, easy to customize, great accessibility. Cons: more initial setup, need to maintain the components ourselves.

2. **Material UI (MUI)** — The most popular React component library. Comprehensive set of components with built-in theming. Pros: mature, extensive documentation, large community. Cons: large bundle size, opinionated design language that's hard to customize, vendor lock-in.

3. **Chakra UI** — A component library focused on developer experience with a prop-based styling API. Pros: good DX, accessible by default. Cons: runtime CSS-in-JS has performance implications, smaller ecosystem than MUI.

We chose option 1 (shadcn/ui with Radix) because it gives us the most control over our component code while still providing excellent accessibility through Radix primitives. The Tailwind CSS approach aligns with our existing styling strategy and keeps the bundle size minimal. The main tradeoff is that we need to maintain these components ourselves, but the team felt this was worthwhile for the level of customization we need.

## Technical Debt Inventory (January 2026)

A summary of the major technical debt items identified during our quarterly review:

The authentication system was originally implemented in a rush for the initial launch and has accumulated significant complexity. The token refresh logic is spread across three different files with inconsistent error handling. The WebSocket authentication is handled separately from the HTTP authentication, leading to the reconnection race condition we're currently experiencing. This needs a comprehensive refactoring, but the team is hesitant to touch it until we have better test coverage on the auth flows.

The test suite has grown organically and has several inconsistencies. Some tests use the old Enzyme library while newer tests use Testing Library. The mocking approach varies between test files — some use jest.mock, others use manual mocks, and a few use MSW for network mocking. We should standardize on Testing Library and MSW and gradually migrate the remaining Enzyme tests.

The database migration history has some issues. Several early migrations contain both schema changes and data transformations, which makes them slow to run and difficult to rollback. Going forward, all migrations should contain only schema changes. Data transformations should be handled by separate scripts that can be run independently.

The frontend build pipeline was recently migrated from Webpack to Vite, which resolved the slow build times. However, there are still some leftover Webpack-specific configurations and polyfills that should be cleaned up.
