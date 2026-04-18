# PostgreSQL Race Condition

## Problem

A Node.js API endpoint increments a counter in PostgreSQL. Under concurrent requests, it sometimes returns the same value to multiple callers.

How do you fix this race condition?
