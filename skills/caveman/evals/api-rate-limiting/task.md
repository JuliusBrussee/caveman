# API Rate Limiting

## Task

Design a rate limiting system for a REST API. Requirements: 100 requests per minute per API key, return proper HTTP headers (X-RateLimit-*), return 429 when exceeded. The API runs on multiple server instances behind a load balancer.
