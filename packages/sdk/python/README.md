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

See [Python SDK documentation](https://caveman.so/docs/sdk/python).
