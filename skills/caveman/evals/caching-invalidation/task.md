# Cache Invalidation Strategies

## Task

A web app caches user profile data. When a user updates their profile, other users sometimes see stale data for several minutes. The architecture uses: app servers → Redis cache (5-minute TTL) → PostgreSQL.

Explain the different cache invalidation strategies and recommend which one to use here.
