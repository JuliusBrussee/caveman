# CLAUDE.md — Taskflow Project

## Overview

Taskflow full-stack task management app. Teams create, assign, track, manage tasks across projects with real-time collaboration. Started internal tool, now open-source.

Active dev focus: improve performance, add integrations (Slack, GitHub, Jira).

## Architecture

Three-tier architecture: frontend, backend API, data layer.

### Frontend

React 18 + TypeScript. Next.js 14 (SSR + API routes).
UI: Radix UI + Tailwind CSS.
State: React Context (global), TanStack Query (server state + caching).

Code structure:

* `src/app/` — App Router
* `src/components/` — shared components
* `src/lib/` — utilities
* `src/types/` — type definitions

### Backend

Node.js + Express API, port 3001 (dev).
Pattern: controller-service-repository.

* controllers handle HTTP
* services contain business logic
* repositories manage DB access

Structure under `server/src/`:

* `server/src/controllers/` — route handlers + validation
* `server/src/services/` — business logic
* `server/src/repositories/` — DB queries (Knex.js)
* `server/src/middleware/` — auth, rate limit, errors
* `server/src/jobs/` — background jobs (BullMQ)

### Database

PostgreSQL 15 primary DB.
Migrations: Knex.js in `server/migrations/`.

Tables: users, teams, projects, tasks, comments, attachments, audit logs.

Redis: caching, sessions, BullMQ message broker.

### Infrastructure

AWS deploy using ECS Fargate.

CI/CD (GitHub Actions):

1. PR: lint, type-check, unit + integration tests
2. Merge to main: build Docker, push to ECR, deploy staging
3. Release tag: promote staging → production

## Key Conventions

### Code Style

ESLint (Airbnb + TypeScript), Prettier formatting.
Pre-commit: Husky + lint-staged run linters.

Rules:

* Use strict TypeScript
* Avoid `any`, if used explain why
* Prefer interfaces over type aliases
* Use discriminated unions for state

### Testing

Test suite:

* Unit: `*.test.ts`, Vitest + Testing Library
* Integration: `tests/integration/`, real PostgreSQL (Docker), run `npm run test:integration`
* E2E: `tests/e2e/`, Playwright, CI only

Rules:

* Test behavior, not implementation
* Mock external services
* Do NOT mock DB in integration tests

## Git Workflow

Trunk-based development.
Short-lived feature branches → PR → merge to `main`.

Branch format: `<type>/<ticket-id>-<short-description>`
Example: `feat/TF-123-add-slack-integration`

Commits: Conventional Commits — `type(scope): description`
Types: feat, fix, refactor, test, docs, chore, perf

Rules:

* Require ≥1 approval
* CI must pass
* Prefer squash merge

## Common Commands

```bash
# Development
npm run dev              # Start frontend + backend in parallel
npm run dev:frontend     # Start only Next.js dev server
npm run dev:backend      # Start only Express API server

# Testing
npm run test             # Run unit tests with Vitest
npm run test:watch       # Run tests in watch mode
npm run test:integration # Run integration tests (requires Docker)
npm run test:e2e         # Run Playwright E2E tests

# Database
npm run db:migrate       # Run pending migrations
npm run db:rollback      # Rollback last migration batch
npm run db:seed          # Seed database with sample data
npm run db:reset         # Drop, recreate, migrate, and seed

# Build & Deploy
npm run build            # Build frontend and backend
npm run lint             # Run ESLint on all files
npm run typecheck        # Run TypeScript compiler checks
docker compose up -d     # Start all services locally with Docker
```

## Environment Variables

Required env vars. Copy `.env.example` → `.env.local`.

* `DATABASE_URL` — PostgreSQL connection string (`postgresql://user:pass@localhost:5432/taskflow`)
* `REDIS_URL` — Redis connection string (`redis://localhost:6379`)
* `JWT_SECRET` — JWT signing key (≥32 chars)
* `NEXT_PUBLIC_API_URL` — API URL (`http://localhost:3001`)
* `SLACK_WEBHOOK_URL` — optional Slack webhook
* `GITHUB_TOKEN` — optional GitHub token

## Known Issues

1. WebSocket reconnection fails after network drop. Race condition with auth refresh. Issue TF-456

2. Large uploads (>10MB) timeout on slow network. Need chunked upload. Planned next sprint

3. Dashboard slow >500 tasks. Need query optimization + virtual scrolling. Issue TF-489

4. Timezone issue. Stored UTC, displayed server timezone, not user local. Need API + frontend fix

## Team

* Alex Chen — tech lead, backend + infra
* Maya Patel — frontend lead, design system
* Jordan Kim — full-stack, Slack + GitHub integrations
* Sam Rivera — backend, dashboard performance

