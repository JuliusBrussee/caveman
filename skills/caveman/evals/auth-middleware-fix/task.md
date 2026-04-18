# Express Auth Middleware Bug

## Problem

An Express auth middleware is letting expired JWT tokens through. The expiry check compares `Date.now()` to the token's `exp` field.

Identify the bug and provide the fix.
