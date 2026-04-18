# Python Decorator for Auth

## Task

Write a Python decorator `@require_role("admin")` for a Flask API that checks if the current user (from a JWT in the Authorization header) has the required role. Return 401 if no token, 403 if wrong role. The decorator should work with any role string and preserve the wrapped function's metadata.
