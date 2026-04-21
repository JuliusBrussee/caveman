# API Integration Guide

## Authentication

All API requests include valid JWT in Authorization header.
Get token from login endpoint using credentials.
If expired, use refresh token to get new access token, retry request.

Auth example:

```typescript
const login = async (email: string, password: string) => {
  const response = await fetch('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  });
  const { accessToken, refreshToken } = await response.json();
  return { accessToken, refreshToken };
};
```

Access token expires in 15 min.
On 401 → refresh token.

```typescript
const refreshAccessToken = async (refreshToken: string) => {
  const response = await fetch('/api/auth/refresh', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refreshToken }),
  });
  if (!response.ok) throw new Error('Refresh failed');
  const { accessToken } = await response.json();
  return accessToken;
};
```

## Creating Tasks

Create task → POST `/api/v2/tasks`.

Required: `projectId`, `title`
Optional: others use defaults

`priority`: 1 (low) → 5 (high), default 3

```typescript
interface CreateTaskPayload {
  projectId: string;
  title: string;
  description?: string;
  assigneeId?: string;
  priority?: 1 | 2 | 3 | 4 | 5;
  dueDate?: string; // ISO 8601 format
  labels?: string[];
}

const createTask = async (payload: CreateTaskPayload, token: string) => {
  const response = await fetch('/api/v2/tasks', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify(payload),
  });
  return response.json();
};
```

Response includes: `id`, `createdAt`, `status="todo"`.

## Error Handling

All errors return:

* `code` — machine-readable
* `message` — human-readable
* `details` — optional extra info

Common errors:

* `AUTH_TOKEN_EXPIRED` — refresh + retry
* `AUTH_TOKEN_INVALID` — login again
* `VALIDATION_ERROR` — check `details`
* `NOT_FOUND` — resource missing / no access
* `RATE_LIMIT_EXCEEDED` — wait (`Retry-After`)

Pattern:

```typescript
class ApiError extends Error {
  constructor(
    public code: string,
    public status: number,
    message: string,
    public details?: Record<string, string[]>
  ) {
    super(message);
  }
}

const apiClient = async (url: string, options: RequestInit = {}) => {
  const response = await fetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  });

  if (!response.ok) {
    const error = await response.json();
    throw new ApiError(error.code, response.status, error.message, error.details);
  }

  return response.json();
};
```

## Pagination

All list endpoints use cursor-based pagination.
Better than offset for consistency with concurrent changes.

Response includes `cursor`.
Pass as query param for next page.

Defaults:

* page size: 50
* max: 100 (`limit`)

Fetch all tasks:

```typescript
const fetchAllTasks = async (projectId: string, token: string) => {
  let cursor: string | undefined;
  const allTasks = [];

  do {
    const params = new URLSearchParams({ limit: '50' });
    if (cursor) params.set('cursor', cursor);

    const response = await apiClient(
      `/api/v2/projects/${projectId}/tasks?${params}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );

    allTasks.push(...response.data);
    cursor = response.cursor;
  } while (cursor);

  return allTasks;
};
```

## Rate Limiting

Limits:

* Authenticated: 100 req/min
* Unauthenticated: 20 req/min

On exceed → 429 + `Retry-After`.

Client strategy:

* Use exponential backoff
* Start with `Retry-After`
* Double each retry
* Max wait: 60s

Prevents thundering herd.

## Webhooks

Supports outgoing webhooks for events:

* task created, updated, deleted, assigned, status change

Configured in project settings.
Sends POST with event payload.

Security:

* Header: `X-Taskflow-Signature`
* HMAC-SHA256 of body using webhook secret
* Always verify before processing

```typescript
import crypto from 'crypto';

const verifyWebhookSignature = (
  payload: string,
  signature: string,
  secret: string
): boolean => {
  const expected = crypto
    .createHmac('sha256', secret)
    .update(payload)
    .digest('hex');
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(expected)
  );
};
```
