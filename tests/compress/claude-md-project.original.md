# CLAUDE.md — Taskflow Project

## Overview

Taskflow is a full-stack task management application built with a modern web stack. The application allows teams to create, assign, track, and manage tasks across multiple projects with real-time collaboration features. It was originally created as an internal tool for our engineering team and has since been open-sourced.

The project is currently in active development with a focus on improving performance and adding integration capabilities with third-party services like Slack, GitHub, and Jira.

## Architecture

The application follows a standard three-tier architecture with clear separation of concerns between the frontend, backend API, and data layer.

### Frontend

The frontend is a React 18 application written in TypeScript. We use Next.js 14 as the meta-framework for server-side rendering and API routes. The UI component library is built on top of Radix UI primitives with Tailwind CSS for styling. State management is handled through a combination of React Context for global state and TanStack Query (formerly React Query) for server state management and caching.

The frontend source code lives in `src/app/` following the Next.js App Router convention. Shared components are in `src/components/`, utility functions in `src/lib/`, and type definitions in `src/types/`.

### Backend

The backend API is built with Node.js and Express, running on port 3001 in development. It follows a controller-service-repository pattern where controllers handle HTTP concerns, services contain business logic, and repositories manage database access. The API code lives in `server/src/` with the following structure:

- `server/src/controllers/` — Route handlers and request validation
- `server/src/services/` — Business logic and orchestration
- `server/src/repositories/` — Database queries using Knex.js query builder
- `server/src/middleware/` — Authentication, rate limiting, error handling
- `server/src/jobs/` — Background job processors using BullMQ

### Database

We use PostgreSQL 15 as the primary database. Migrations are managed with Knex.js and live in `server/migrations/`. The database schema includes tables for users, teams, projects, tasks, comments, attachments, and audit logs. Redis is used for caching, session storage, and as the message broker for BullMQ background jobs.

### Infrastructure

The application is deployed on AWS using ECS Fargate for containerized services. The CI/CD pipeline runs on GitHub Actions with the following workflow:

1. On every pull request: lint, type-check, unit tests, and integration tests
2. On merge to main: build Docker images, push to ECR, deploy to staging
3. On release tag: promote staging image to production

## Key Conventions

### Code Style

We use ESLint with a custom configuration that extends the Airbnb style guide with TypeScript-specific rules. Prettier is configured for consistent formatting. The pre-commit hook runs both linters automatically using Husky and lint-staged.

All TypeScript code should use strict mode. Avoid using `any` type unless absolutely necessary, and if you do, add a comment explaining why. Prefer interfaces over type aliases for object shapes, and use discriminated unions for state management.

### Testing

We maintain a comprehensive test suite with the following structure:

- **Unit tests**: Located alongside source files as `*.test.ts`. Use Vitest as the test runner with Testing Library for component tests. Aim for high coverage on business logic and utility functions.
- **Integration tests**: Located in `tests/integration/`. These tests hit a real PostgreSQL database (managed by Docker Compose) and verify end-to-end API behavior. Run with `npm run test:integration`.
- **E2E tests**: Located in `tests/e2e/`. Use Playwright for browser automation. These are slower and run only in CI, not as part of the pre-commit hook.

When writing tests, prefer testing behavior over implementation details. Mock external services but never mock the database in integration tests — we learned this the hard way when mocked tests passed but a production migration broke things.

### Git Workflow

We use a trunk-based development model. All development happens on short-lived feature branches that are merged into `main` via pull requests. Branch names should follow the pattern `<type>/<ticket-id>-<short-description>` (e.g., `feat/TF-123-add-slack-integration`).

Commit messages follow Conventional Commits format: `type(scope): description`. The types we use are: feat, fix, refactor, test, docs, chore, perf.

Pull requests require at least one approval from a team member. The CI pipeline must pass before merging. We prefer squash merges for feature branches to keep the main branch history clean.

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

The application requires the following environment variables. Copy `.env.example` to `.env.local` and fill in the values:

- `DATABASE_URL` — PostgreSQL connection string (e.g., `postgresql://user:pass@localhost:5432/taskflow`)
- `REDIS_URL` — Redis connection string (e.g., `redis://localhost:6379`)
- `JWT_SECRET` — Secret key for signing JWT tokens (minimum 32 characters)
- `NEXT_PUBLIC_API_URL` — Backend API URL for the frontend (e.g., `http://localhost:3001`)
- `SLACK_WEBHOOK_URL` — Optional: Slack webhook for notifications
- `GITHUB_TOKEN` — Optional: GitHub personal access token for issue sync

## Known Issues

There are a few known issues that the team is currently aware of and working on:

1. **WebSocket reconnection** — The real-time collaboration feature sometimes fails to reconnect after a network interruption. The client-side reconnection logic has a race condition with the authentication refresh flow. Tracked in issue TF-456.

2. **Large file uploads** — Attachments larger than 10MB occasionally timeout on slower connections. The upload endpoint needs to be refactored to support chunked uploads. This is planned for the next sprint.

3. **Dashboard performance** — The main dashboard becomes sluggish when a project has more than 500 tasks. The query needs optimization and we should implement virtual scrolling on the frontend. Issue TF-489.

4. **Timezone handling** — Due dates are stored in UTC but displayed in the server's timezone instead of the user's local timezone. This causes confusion for distributed teams. The fix requires updating both the API response serialization and the frontend date formatting utilities.

## Team

- **Alex Chen** — Tech lead, owns backend architecture and infrastructure
- **Maya Patel** — Frontend lead, owns component library and design system
- **Jordan Kim** — Full-stack, currently focused on the Slack and GitHub integrations
- **Sam Rivera** — Backend, currently working on performance optimizations for the dashboard queries
