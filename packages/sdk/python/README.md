# caveman

Stdlib-only Python client for Caveman gateway cooperation.

Distribution name: `caveman-sdk`. Import package: `caveman_cloud`. Install from
PyPI:

```bash
python -m pip install caveman-sdk
```

Requires Python 3.13 or newer.

For editable work from this source directory, use `python -m pip install -e .`.

```python
import os
from caveman_cloud import Cave

cave = Cave(
    api_key=os.environ["CAVE_API_KEY"],
    base_url="http://127.0.0.1:8787",
    agent="support-agent",
)

result = cave.compress("large payload")
print(result.output, result.basis)  # basis is inferred
```

Main surfaces: provider clients, `compress`, deferred tool search, reversible
checkpoints and artifacts, retry-loop interruption, runtime policy, and a
stdlib-only OTLP/JSON exporter. Async jobs are reserved and fail locally with
`cave_async_jobs_unavailable`; they send no request.

Package is MIT licensed. Connected calls need a Caveman gateway key; local Engine
compression remains accountless and ships through the separate Caveman runtime.

## Framework middleware runtime

The `caveman_cloud.middleware` entry connects native framework adapters to an
existing local runtime. It installs no framework or inference client.

```python
from caveman_cloud.middleware import CallReport, MiddlewareRuntime

def report(event: CallReport) -> None:
    print(event.status, event.reason)

with MiddlewareRuntime(
    endpoint="http://127.0.0.1:8787", on_report=report,
) as runtime:
    runtime.ready()
    # Pass runtime and an explicit conversation scope to a native adapter.
```

Async applications can use `AsyncMiddlewareRuntime` with the same synchronous
metadata callback. Adapters report `applied`, `reused`, `skipped`, `recorded`, or
`disabled` after deciding which request view to use. Frozen `CallReport` values
contain transform IDs, replacement/reuse counts, and optional adapter/call IDs,
never original content. `runtime.last_report` holds the latest report across
this runtime, without a history. Callback exceptions do not affect inference.
Reports describe projection decisions; usage and billing evidence are separate.
Calling low-level `optimize()` only prepares a plan and does not claim application.

See [Python SDK documentation](https://caveman.so/docs/sdk/python).
