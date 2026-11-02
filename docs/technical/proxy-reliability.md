# Local proxy reliability audit

Audited 2026-09-05. Scope: the standalone proxy, shared provider adapters, and
local CLI wrapper. This is an engineering verification record, not an uptime
SLA or a claim that every provider endpoint has been certified.

Shutdown and launcher signals were re-audited on 2026-10-25 using isolated local
HTTP fixtures and subprocesses. Native Windows signal behavior remains a
separate host verification requirement.

## Findings and changes

| Failure mechanism | Changed behavior | Regression evidence |
| --- | --- | --- |
| A wrap-owned listener retired after 30 minutes of native inactivity. Missing hooks, an exited wrapper, or suspended heartbeats could leave a live agent pointing at a closed port. | The serve loop has no idle-exit path, including when an older installation exports `CAVEMAN_NATIVE_IDLE_TIMEOUT`. Only an explicit stop or process failure ends it. | `TestServeDoesNotExpire`: real subprocess, isolated databases, accelerated legacy timeout, no heartbeats. |
| A new wrapper could SIGTERM a shared proxy to change mode/recovery if it found no live wrapper marker. Native and resumed sessions do not necessarily own those markers. | Wrappers never restart a shared listener. Incompatible new runs launch directly with their own provider configuration. Failed local startup also launches directly rather than exporting a dead endpoint. | `wrap-restart.runtime.mjs`: real proxy PIDs/generations remain unchanged, no signal, direct fallback, failed-start route removal, foreign-listener protection. |
| Go `http.Client.Timeout` defaulted to 900,000 ms and includes reading the response body. An active stream could therefore expire at 15 minutes. | Default is `0`: no total generation deadline. A positive `CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS` remains an explicit operator cap. Caller cancellation still cancels upstream. An upstream that connects and then sends nothing is bounded instead by the transport's response-header deadline (`CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS`, default 900,000 ms), which does not cover the body. | Default-client assertion; production SSRF transport with delayed SSE and explicit-cap control; cancellation tests; `TestDefaultUpstreamClientBoundsResponseHeaders` (silent upstream answers 502 instead of hanging). |
| A response could stream even when the request did not expose a readable JSON `stream` flag. The proxy buffered such responses. | SSE, Bedrock event streams, and recognized JSON-line streams are detected from response `Content-Type`. Headers flush immediately; chunks and unknown events pass through unchanged. | `TestProviderStreamsFlushBeforeCompletion`: nine routes, gated headers/body/end, no JSON stream flag. |
| The shared header mapper dropped `Content-Encoding` from encoded requests. | Encoded request bytes retain the header and bypass optimization. | `TestEncodedRequestPassesThroughWithoutTransforms`: gzip body preserved in record, compress, pixel, and active modes. |
| Interrupted upstream streams became a clean downstream EOF, while telemetry showed no error. | The copy error is recorded; HTTP framing is aborted after committed headers. No fabricated completion or replay of a partial stream. | `TestInterruptedStreamsAbortClientFramingWithoutReplay`: Anthropic and ChatGPT routes, HTTP/1.1 and HTTP/2. |
| Transport and response-read failures were retried on the assumption that no response meant no inference occurred. | Explicit proxy retries only cover connection-setup failures: the TCP dial, and the connection to an outbound HTTP proxy, which Go reports as `proxyconnect` rather than `dial`. Ambiguous upload/header failures and truncated bodies are not silently replayed. A complete transformed-request 4xx can still fall back once to original bytes. | Dial retry, proxy-connect retry, ambiguous failure/no replay (including a refused `CONNECT` tunnel), and truncated gzip/no replay tests; existing original-body fallback tests. |
| A nil telemetry sink could panic after forwarding a successful response. | An omitted sink skips recording, matching the existing ChatGPT path. | Streaming and encoded-request regressions run without a sink. |
| Shutdown stopped waiting after five seconds, then exited with active provider streams and stores still in use. Repeated stop signals remained intercepted. | The first SIGINT/SIGTERM closes the listener and drains accepted requests without a new generation deadline. A second stop signal uses normal OS termination. | `TestServeShutdown`: accepted SSE response remains alive beyond five seconds and finishes intact; a second signal interrupts a blocked drain. |
| A process-directed SIGINT was swallowed by the non-interactive native agent launcher; `caveman start` abandoned its proxy on supervisor termination and reported a signal-killed proxy as success. A TTY-attached `start` also swallowed process-directed SIGINT. | Non-interactive agent launchers forward stop signals. On POSIX, `start` owns a separate proxy process group and forwards signals once, waits for drain, and propagates signal failures as nonzero exit codes. | `agent-shortcut.runtime.mjs` and `start-lifecycle.runtime.mjs`: real stub processes, targeted SIGINT/SIGTERM, drain completion, and signal failure status. Real PTY fixtures verify both PID-directed and group-directed SIGINT, exactly one delivery, and no surviving proxy. |

Request-body read failures also receive a distinct error instead of being
misreported as oversized requests. Old native correlation entries are pruned
when new events arrive; aging that bookkeeping never closes the API listener.

## Provider compatibility evidence

The stream matrix uses real local HTTP servers and the real adapters for:
Anthropic Messages, OpenAI Chat Completions, OpenAI Responses, Azure OpenAI,
Gemini, Vertex, OpenAI-compatible mounts, Bedrock, and the dedicated ChatGPT
subscription route. Bedrock's synthetic bytes verify transport delivery, not AWS
EventStream decoding. Existing provider suites separately exercise wire shapes,
auth/signing, routing, transforms, and usage parsing.

Fresh inbound OAuth is resolved on every call. Tests send an expired test token
and then a replacement through the same server, preserving the provider's 401
body and subsequent success on Anthropic, OpenAI, Gemini, the named compatible
Messages mount, and ChatGPT. The ChatGPT account header remains intact. The proxy
does not cache provider access tokens or own the agent's refresh process.

These are local compatibility and failure-injection tests. No paid provider
traffic, real OAuth refresh, multi-day soak, production rollout, or blanket
provider certification is claimed.

## What LangWatch and Headroom establish

[LangWatch's Claude Code documentation](https://langwatch.ai/docs/ai-gateway/cli/claude-code)
describes an Anthropic raw-forward path. It explicitly says a provider failure
after the first streamed chunk ends that stream; fallback is possible on a new
turn. It also documents cross-provider tool-call and caching differences.
This supports preserving the wire protocol and avoiding mid-stream failover;
it does not establish zero-error operation.

Headroom has documented the same long-turn failure family:
[issue #1261](https://github.com/headroomlabs-ai/headroom/issues/1261) describes
buffered Anthropic calls hitting a total timeout, and
[issue #2465](https://github.com/headroomlabs-ai/headroom/issues/2465) describes
server-side recovery converting streaming calls into buffered requests with no
keepalive. Both issues were closed when checked; they are historical failure
evidence, not claims about current Headroom behavior. Caveman's streaming
compression path uses agent-side MCP recovery and keeps the upstream stream.

[Anthropic's streaming contract](https://platform.claude.com/docs/en/build-with-claude/streaming)
includes ping, error, and future event types. The proxy forwards those bytes
rather than inventing provider events or hiding provider errors.
[Go's HTTP client contract](https://pkg.go.dev/net/http#Client) explains that a
client timeout remains active while reading the response body.

## Original validation — 2026-09-05

For the current patch's full-suite results, skips, and remaining work, see the
[2026-10-25 maintenance audit](maintenance-audit-2026-10-25.md). The results below
describe the earlier audit and are retained as historical evidence.

- `go test -json ./proxy/...`: 1,331 passing test cases/subtests, 22 passing
  packages; one existing optional local-fixture diagnostic skipped, two packages
  have no tests.
- `go test -race ./proxy/internal/gateway ./proxy/internal/standalone ./proxy/internal/nativeruntime ./proxy/cmd/caveman-proxy`:
  404 passing cases/subtests; one existing optional diagnostic skipped.
- `pnpm --dir packages/cli exec tsc`: passed.
- Wrapper restart/gate/subscription recovery runtime suites: 50 tests passed.
- Broader wrapper/hook run: 143/150 initially passed. The raw-wrapper fixture
  expected injection without a live listener; it now provides an owned listener
  and its targeted rerun passes. Six hook tests still conflict with concurrent
  Core/skill packaging edits outside this patch. Enabling Core explicitly makes
  five pass; the remaining test expects the replaced `# Lean build` heading.
  No clean full-CLI-suite result is claimed.
- Proxy binary built successfully and its `version --json` command ran. The
  standalone binary is a development artifact; no installed process was replaced.

## Operating limits

There is no proxy session TTL. An idle HTTP keep-alive connection can still be
closed and reopened normally; this is separate from listener or generation
lifetime. TCP/TLS setup remains bounded, as do the wait for the first upstream
response header, inbound headers/uploads, and request/response buffering. Default general request buffer is 32 MiB; default
non-streaming response buffer is 64 MiB. The ChatGPT route reads a
compress-eligible request whole up to the same request limit (a zstd body
decodes up to it too) and streams a larger one through untransformed.

On POSIX hosts, SIGINT or SIGTERM initiates graceful shutdown: new connections
stop, accepted requests finish, then persistence closes. Send a second stop
signal to terminate immediately if a request does not complete. This follows
[Go's HTTP shutdown contract](https://pkg.go.dev/net/http#Server.Shutdown).
The returned stop function from [Go's signal context](https://pkg.go.dev/os/signal#NotifyContext)
is called before draining so a second stop signal restores normal OS termination.
On POSIX, `caveman start` uses [Node's detached child process group](https://nodejs.org/api/child_process.html#optionsdetached)
without unreferencing the child: the launcher still waits, and terminal broadcasts
cannot deliver the same interrupt directly and through the forwarding handler.
POSIX wrapped agents also receive terminal broadcasts directly, so the launcher
does not forward TTY SIGINT/SIGQUIT a second time. Noninteractive launchers still
forward process-directed signals. Wrappers retain their existing ten-second
and second-stop escape, cleaning temporary credentials before forced exit.
On Windows, `start`, native agent shortcuts, and wrapped agents retain the shared
console and wait for the child after Ctrl+C, Ctrl+Break, or console close. Windows
[broadcasts console events to attached processes](https://learn.microsoft.com/en-us/windows/console/ctrl-c-and-ctrl-break-signals),
including when stdin is redirected; forwarding them with
[Node's signal emulation](https://nodejs.org/api/process.html#signal-events)
could force-terminate the child. Local tests exercise this dispatch policy with
an injected Windows platform; native Windows console execution remains unverified.
Console close has an OS-imposed cleanup deadline. A supervisor or OS force-kill
can interrupt requests on every platform.

Provider outages, DNS/network loss, sleep-induced socket loss, invalid or expired
provider credentials, provider context/rate limits, process termination, and
unsupported routes can still produce errors. Explicit route/auth/security
allowlists remain enforced. Native OpenAI Responses WebSocket upgrades are
supported on exact `GET /v1/responses` and `GET /openai/v1/responses`, including
normalized `/w` paths. They use local authentication, provider credentials, and
the guarded upstream transport; frames pass through without optimization,
capture, or usage accounting. Server shutdown closes upgraded connections.
Browser-originated handshakes are refused so a webpage cannot spend a configured
local provider key. See [OpenAI's Responses connection contract](https://developers.openai.com/api/reference/resources/responses/methods/connect)
and [RFC 6455's origin considerations](https://www.rfc-editor.org/rfc/rfc6455#section-10.2).
Other provider and compatible-mount WebSocket routes remain unsupported. Unknown
routes remain closed. Explicit request deadlines still take effect when configured.

Switching a conversation between optimized HTTP requests and opaque WebSocket
passthrough can replay raw history instead of the memoized transformed bytes.
Cross-transport cache-prefix equivalence is unsupported and unverified. The
WebSocket tests prove byte relay within that transport, not cache continuity
when changing transports.

An already-running agent cannot switch away from a process that the OS killed
without client cooperation. Native prompt hooks can revive a missing local proxy;
there is no transparent mid-stream migration or universal daemon supervision.
These source changes require rebuilding/updating both the proxy and CLI. They
do not replace a running installation or publish a release automatically.
