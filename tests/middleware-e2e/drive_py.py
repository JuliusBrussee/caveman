"""One OpenAI chat call through the certified openai adapter against a live runtime.

The provider is a local fake OpenAI server on 127.0.0.1 (no network). After the
call, the adapter's own caveman_retrieve executor recovers the handle the
provider was shown.

    python drive_py.py --base URL --token T --expect compress|passthrough

Prints one JSON line; exits 1 when the expectation fails.
"""
import argparse
import copy
import hashlib
import json
import re
import secrets
import threading
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ORIGINAL = "".join(f"[INFO] reading row {i}: café 🌍 exact-value-{i:03d} with verbose repeated details\r\n" for i in range(150))
ORIGINAL += "[ERROR] preserve this diagnostic exactly\r\n"


def sha256(text):
    return hashlib.sha256(text.encode()).hexdigest()


def fake_openai(received):
    class Provider(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
            body = json.dumps({"id": "c", "object": "chat.completion", "created": 0, "model": "m", "choices": [
                {"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def main():
    parser = argparse.ArgumentParser()
    for name in ("--base", "--token", "--expect"):
        parser.add_argument(name, required=True)
    args = parser.parse_args()

    from openai import OpenAI
    from caveman_cloud.middleware import MiddlewareRuntime, Scope
    from caveman_middleware.openai import with_caveman_openai_tools

    received, reports, out = [], [], {}
    server = fake_openai(received)
    runtime = MiddlewareRuntime(endpoint=args.base, token=args.token, deadline_ms=10000, retrieve_deadline_ms=10000,
                                on_report=lambda report: reports.append(f"{report.status}:{report.reason}"))
    messages = [{"role": "user", "content": "Summarize this log."},
                {"role": "assistant", "tool_calls": [{"type": "function", "id": "call-1", "function": {"name": "read_log", "arguments": "{}"}}]},
                {"role": "tool", "tool_call_id": "call-1", "content": ORIGINAL}]
    before = copy.deepcopy(messages)
    tools = [{"type": "function", "function": {"name": "read_log", "description": "Read the log", "parameters": {"type": "object", "properties": {}}}}]
    try:
        with OpenAI(api_key="test", base_url=f"http://127.0.0.1:{server.server_port}/v1", max_retries=0) as client:
            loop = with_caveman_openai_tools(client, runtime=runtime, scope=Scope("e2e", "py-" + secrets.token_hex(6)),
                                             protocol="openai-chat", tools=tools, functions={"read_log": lambda _: ORIGINAL})
            loop.client.chat.completions.create(model="m", tools=loop.tools, messages=messages)
            sent = received[0]["messages"][-1]["content"]
            handle = re.search(r"handle=(cmw_[a-f0-9]{48})\]", sent)
            recovered = loop.functions["caveman_retrieve"]({"handle": handle.group(1)})["text"] if handle else None
        features = None
        if args.expect == "compress":
            features = runtime.ready().get("features")  # the capabilities view this client negotiated
        out = {"compressed": sent.startswith("[caveman: shortened;"), "recovered": recovered is not None and sha256(recovered) == sha256(ORIGINAL),
               "passthrough": sent == ORIGINAL, "intact": messages == before, "sent_bytes": len(sent.encode()),
               "original_bytes": len(ORIGINAL.encode()), "runtime_features": features, "reports": reports}
        if args.expect == "compress":
            assert out["compressed"], "the provider saw the original: no compression"
            assert out["sent_bytes"] < out["original_bytes"], "the replacement is not smaller"
            assert out["recovered"], "caveman_retrieve did not return the exact original"
        else:
            assert args.expect == "passthrough"
            assert out["passthrough"], "the provider did not see the exact original"
        assert out["intact"], "the caller's history changed"
        print(json.dumps(out))
        return 0
    except Exception:  # report the failure as the JSON line the harness reads
        print(json.dumps({"error": traceback.format_exc(), **out, "reports": reports}))
        return 1
    finally:
        runtime.close()
        server.shutdown()


if __name__ == "__main__":
    raise SystemExit(main())
