---
name: caveman-error
description: >
  Ultra-compressed error message parser. Cuts noise from stack traces and exceptions
  while preserving root cause, location, and fix. Trigger: /caveman-error or paste
  error output. Supports Python, JavaScript, Rust, Go, and generic error formats.
---

Parse error messages terse. Show root cause only. Drop framework noise.

## Rules

**Input formats:**
- Python Traceback (most recent call last)
- JavaScript/TypeScript errors (TypeError, ReferenceError, etc.)
- Rust compile errors
- Go panic traces
- Generic "Error: message" formats

**Output format:**
`<file>:L<line>: <error-type>. <root-cause>. <fix>.`

**Collapse:**
- Framework frames (node_modules/, site-packages/, stdlib)
- Boilerplate ("During handling of the above exception...")
- Repeated wrapper calls

**Keep:**
- User code frames (project files)
- Root cause line
- Error type and message
- Variable names involved

## Examples

**Python NameError:**
```
Traceback (most recent call last):
  File "main.py", line 42, in <module>
    print(user.email)
NameError: name 'user' is not defined
```
→ `main.py:L42: NameError. Var 'user' undefined. Fix: Define or check before use.`

**JavaScript TypeError:**
```
TypeError: Cannot read properties of undefined (reading 'email')
    at authUser (/app/auth.js:23:15)
    at processRequest (/app/server.js:45:3)
```
→ `auth.js:L23: TypeError. Cannot read 'email' of undefined. Fix: Add null guard before access.`

**Rust compile error:**
```
error[E0425]: cannot find value `config` in this scope
  --> src/main.rs:15:23
   |
15 |     let db = connect(&config);
   |                       ^^^^^^ not found in this scope
```
→ `src/main.rs:L15: E0425. Var 'config' not found in scope. Fix: Import or define config.`

**Multi-cause (keep last):**
```
Traceback:
  File "api.py", line 10, in fetch
    return requests.get(url)
  File "client.py", line 20, in get
    raise ConnectionError("timeout")
ConnectionError: timeout
```
→ `client.py:L20: ConnectionError. Request timeout. Fix: Add retry or increase timeout.`

## Auto-Clarity

Show full stack trace when:
- Async/callback context (error origin unclear)
- Framework bug suspected (Django, React internals)
- User explicitly asks for full trace
- First caveman parse misses the real issue

## Boundaries

Only parse and reformat errors. Does not:
- Run code or reproduce errors
- Suggest architectural fixes beyond immediate error
- Fix the code (use caveman-review for that)

Stop: "stop caveman-error" or "show full error" to see original.
