# API Integration Guide

## Authentication

All API requests must include a valid JWT token in the Authorization header. The token is obtained by calling the login endpoint with valid credentials. If the token has expired, the client should use the refresh token to obtain a new access token before retrying the failed request.

Here's how to authenticate:

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

The access token expires after 15 minutes. When you receive a 401 response, you should attempt to refresh the token:

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

To create a new task, you need to send a POST request to the tasks endpoint with the required fields. The `projectId` and `title` fields are required. All other fields are optional and will use sensible defaults if not provided. The `priority` field accepts values from 1 (lowest) to 5 (highest), with 3 being the default.

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

The response will include the created task with a generated `id`, `createdAt` timestamp, and `status` set to "todo" by default.

## Error Handling

The API returns consistent error responses across all endpoints. Every error response includes a `code` field with a machine-readable error identifier and a `message` field with a human-readable description. Some errors also include a `details` field with additional context.

Common error codes you should handle in your client application:

- `AUTH_TOKEN_EXPIRED` — The access token has expired. Refresh it and retry the request.
- `AUTH_TOKEN_INVALID` — The token is malformed or has been tampered with. The user needs to log in again.
- `VALIDATION_ERROR` — The request body failed validation. Check the `details` field for specific field errors.
- `NOT_FOUND` — The requested resource does not exist or the user doesn't have permission to access it.
- `RATE_LIMIT_EXCEEDED` — Too many requests. The `Retry-After` header indicates when you can retry.

Here's a recommended error handling pattern for your API client:

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

All list endpoints support cursor-based pagination. This approach was chosen over offset-based pagination because it provides consistent results even when items are being added or removed concurrently. Each response includes a `cursor` field that should be passed as a query parameter to fetch the next page.

The default page size is 50 items, which can be adjusted using the `limit` query parameter (maximum 100). To fetch all tasks in a project with pagination:

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

The API enforces rate limits to ensure fair usage and protect the service from abuse. Authenticated requests are limited to 100 requests per minute. Unauthenticated requests (such as the login endpoint) are limited to 20 requests per minute. When you exceed the rate limit, the API responds with a 429 status code and includes a `Retry-After` header indicating the number of seconds to wait before making another request.

It is recommended that your client application implements exponential backoff when encountering rate limit errors. Starting with the `Retry-After` value, double the wait time on each subsequent 429 response, up to a maximum of 60 seconds. This prevents thundering herd problems when multiple clients hit the rate limit simultaneously.

## Webhooks

Taskflow supports outgoing webhooks for real-time event notifications. You can configure webhook URLs in the project settings. When an event occurs (task created, updated, deleted, assigned, or status changed), the system sends a POST request to your configured URL with the event payload.

Webhook payloads include an `X-Taskflow-Signature` header containing an HMAC-SHA256 signature of the request body using your webhook secret. Always verify this signature before processing the webhook to ensure the request is authentic.

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
