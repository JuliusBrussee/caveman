"""Metadata-only native usage normalization, independent of provider extras."""

import codecs
import json


def langchain_usage(value):
    if not isinstance(value, dict):
        return None
    def count(n):
        return n if type(n) is int and 0 <= n <= 2**53 - 1 else None
    before, after = count(value.get("input_tokens")), count(value.get("output_tokens"))
    inputs, outputs = value.get("input_token_details", {}), value.get("output_token_details", {})
    return {"provenance": "client_observed_sdk", "complete": before is not None and after is not None,
            "input_tokens": before, "output_tokens": after, "cache_read_tokens": count(inputs.get("cache_read")),
            "cache_write_tokens": count(inputs.get("cache_creation")), "reasoning_tokens": count(outputs.get("reasoning"))}


def usage(value, complete=True):
    if not isinstance(value, dict):
        if not hasattr(value, "model_dump"):
            return None
        value = value.model_dump()
    if not value:
        return None
    def measured(n):
        return n if type(n) is int and 0 <= n <= 2**53 - 1 else None
    input_tokens = measured(value.get("input_tokens", value.get("prompt_tokens", value.get("promptTokenCount", value.get("inputTokens")))))
    output_tokens = measured(value.get("output_tokens", value.get("completion_tokens", value.get("candidatesTokenCount", value.get("outputTokens")))))
    details = value.get("input_tokens_details") or value.get("prompt_tokens_details") or {}
    output_details = value.get("output_tokens_details") or value.get("completion_tokens_details") or {}
    return {"provenance": "client_observed_sdk", "complete": complete and input_tokens is not None and output_tokens is not None,
            "input_tokens": input_tokens, "output_tokens": output_tokens,
            "cache_read_tokens": measured(value.get("cache_read_input_tokens", details.get("cached_tokens", value.get("cachedContentTokenCount", value.get("cacheReadInputTokens"))))),
            "cache_write_tokens": measured(value.get("cache_creation_input_tokens", value.get("cacheWriteInputTokens"))),
            "reasoning_tokens": measured(output_details.get("reasoning_tokens", value.get("thoughtsTokenCount")))}


class UsageReader:
    def __init__(self, sse=False, max_buffer=None):
        self.sse, self.terminal, self.overflow = sse, False, False
        self.max_buffer = max_buffer or (262144 if sse else 2 << 20)
        self.text, self.values = "", {}
        self.decoder = codecs.getincrementaldecoder("utf-8")("replace")

    def feed(self, chunk):
        if self.overflow:
            return
        # Check before decoding/concatenating an arbitrarily large host chunk.
        if len(chunk) > self.max_buffer or len(self.text) * 4 + len(chunk) > self.max_buffer:
            self.text, self.overflow = "", True
            return
        self.text += self.decoder.decode(chunk)
        if len(self.text) > self.max_buffer:
            self.text, self.overflow = "", True
            return
        if self.sse:
            while "\n" in self.text:
                line, self.text = self.text.split("\n", 1)
                if line.startswith("data:"):
                    data = line[5:].strip()
                    if data == "[DONE]":
                        self.terminal = True
                    else:
                        try:
                            self.event(json.loads(data))
                        except (ValueError, TypeError):
                            pass

    def event(self, value):
        if type(value) is not dict:
            return
        nested = value.get("response", value.get("message", value))
        if type(nested) is dict and type(nested.get("usage")) is dict:
            self.values.update(nested["usage"])
        if type(value.get("usageMetadata")) is dict:
            self.values.update(value["usageMetadata"])
        if value.get("type") in ("message_stop", "response.completed") or any(type(c) is dict and c.get("finish_reason") is not None for c in value.get("choices", [])):
            self.terminal = True

    def finish(self):
        if self.overflow:
            return None
        if not self.sse:
            try:
                self.event(json.loads(self.text))
                self.terminal = True
            except (ValueError, TypeError):
                return None
        return usage(self.values, self.terminal)

