// Code generated from the validated practice registry. DO NOT EDIT.
// Public artifact intentionally omits sources, Cave Agent scope, and private paths.
export const PRACTICE_REGISTRY = [
  {
    "id": "prompt-prefix-stability",
    "family": "cache",
    "title": "Cache stable prompt prefixes",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "repeated stable system, tool, or reference prefix",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Keep reusable content before volatile input and use provider-native cache controls supported by the active protocol.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held request with identical model-visible prefix and a token ceiling for uncached input."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never attribute an automatic or caller-created cache hit to Caveman without a provider-causal active path.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-prompt-prefix-stability"
    }
  },
  {
    "id": "context-compression",
    "family": "input_bloat",
    "title": "Compress or checkpoint growing context",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large growing history with small downstream output",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Checkpoint stable history or use the approved recoverable compression path, preserving originals on every failure.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long-context tasks comparing task completion before and after context reduction."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never call local tokenizer deltas measured spend or verified savings.",
    "skill_render": {
      "eligible": false,
      "skill_name": "caveman-practice-context-compression"
    }
  },
  {
    "id": "tool-result-truncation",
    "family": "input_bloat",
    "title": "Bound oversized tool results",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "tool result larger than the task needs",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Project, paginate, summarize, or artifact-page large tool results before they re-enter model context.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held tool tasks where required facts appear near the start, middle, and end of a large result."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never truncate without proving required facts and task outcomes remain available.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-tool-result-truncation"
    }
  },
  {
    "id": "tool-schema-deferral",
    "family": "input_bloat",
    "title": "Load tool schemas on demand",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large tool catalog with sparse per-task use",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Expose a small relevant tool set first and retrieve deferred schemas only when the task requires them.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_called",
      "fixture_template": "Held tasks requiring common, deferred, and no-tool paths with expected tool names."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove a tool schema unless deferred discovery still selects it when needed.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-tool-schema-deferral"
    }
  },
  {
    "id": "toon-reencoding",
    "family": "input_bloat",
    "title": "Re-encode uniform tabular JSON as TOON",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "uniform tabular JSON measured smaller after TOON encoding",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Use TOON only when forced or flag-gated and measured smaller, never for tool-call arguments.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "json_schema",
      "fixture_template": "Held uniform tables decoded back to the expected schema before task-quality grading."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "forced-only, never tool-call args, not byte-safe, inferred until causal proof.",
    "skill_render": {
      "eligible": false,
      "skill_name": "caveman-practice-toon-reencoding"
    }
  },
  {
    "id": "anthropic-cache-breakpoints",
    "family": "cache",
    "title": "Place an Anthropic explicit cache breakpoint",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "stable tool definitions or system instructions repeated across requests",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Add an explicit 5-minute cache marker to the last non-deferred tool, or to the last system block when no eligible tool is loaded; the marker changes only the upstream request.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held request pair with an identical tools-and-system prefix, provider-reported cache usage, and an unchanged model-visible projection."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never describe this explicit static-prefix marker as Anthropic automatic caching or claim a saving from placement alone; its write can be net-negative without reuse.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-cache-breakpoints"
    }
  },
  {
    "id": "anthropic-static-prefix-breakpoint",
    "family": "cache",
    "title": "Place Anthropic breakpoint after static content",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "static prefix followed by request-specific suffix",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Place cache control on the last static content block and keep timestamps, request identifiers, and live input after it.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held requests varying only suffix fields with bounded cache writes."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never describe a breakpoint as effective until provider usage shows reuse.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-static-prefix-breakpoint"
    }
  },
  {
    "id": "anthropic-tool-definitions-cache",
    "family": "cache",
    "title": "Cache stable Anthropic tool definitions",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "stable tool definitions repeated across agent turns",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Mark the last stable tool definition as cacheable and defer rarely used tools without breaking prefix order.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "tool_called",
      "fixture_template": "Held tasks selecting tools before and after cache placement, including one deferred tool."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never change tool availability merely to improve cache metrics.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-tool-definitions-cache"
    }
  },
  {
    "id": "anthropic-cache-ttl-right-sizing",
    "family": "cache",
    "title": "Match Anthropic cache TTL to request cadence",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "stable prefix reused on a known cadence",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Use the shorter lifetime for frequent reuse and test the longer lifetime only when gaps regularly exceed the short window.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "cost_threshold",
      "fixture_template": "Held cadence replay comparing supported TTL configurations under one cost ceiling."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never select a longer TTL from intuition; cache-write cost and actual cadence must support it.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-cache-ttl-right-sizing"
    }
  },
  {
    "id": "anthropic-mixed-ttl-ordering",
    "family": "cache",
    "title": "Order mixed Anthropic cache lifetimes",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "request using both long and short cache lifetimes",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Place longer-lived cache entries before shorter-lived entries and keep each boundary aligned with its content cadence.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "http_status",
      "fixture_template": "Held mixed-TTL request expected to succeed and report separate cache-write buckets."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never reorder model-visible content solely to satisfy TTL ordering without an outcome gate.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-mixed-ttl-ordering"
    }
  },
  {
    "id": "anthropic-cache-observability",
    "family": "cache",
    "title": "Measure Anthropic cache reads and writes",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "cache-enabled traffic without read and write accounting",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Record provider-reported cache creation, cache read, and lifetime-specific write fields before tuning placement.",
      "apply": "manual"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held cached response requiring cache usage fields to exist with non-negative values."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never estimate cache effectiveness from request count when provider usage fields are available.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-cache-observability"
    }
  },
  {
    "id": "anthropic-message-batch-offload",
    "family": "routing",
    "title": "Batch non-interactive Anthropic work",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "bulk or scheduled Messages work with no interactive latency need",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Send eligible independent requests through Message Batches and preserve per-item identifiers, errors, and completion checks.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held synchronous and batch outputs normalized to the same expected task result."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never batch latency-sensitive work or treat accepted batch jobs as completed results.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-message-batch-offload"
    }
  },
  {
    "id": "anthropic-token-count-preflight",
    "family": "input_bloat",
    "title": "Preflight large Anthropic requests with provider token counts",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large request near a policy, cost, or context threshold",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Call the provider token-count endpoint for sampled or boundary requests before choosing a reduction path.",
      "apply": "manual"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Held request whose provider count must remain below the configured input ceiling."
    },
    "experiment": {
      "method": "shadow",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never substitute a local tokenizer estimate for provider-measured baseline tokens.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-token-count-preflight"
    }
  },
  {
    "id": "anthropic-strict-tool-use",
    "family": "reliability",
    "title": "Constrain Anthropic tool arguments",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "tool calls retried after invalid arguments",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Use strict tool schemas for stable contracts and reject invalid arguments without replaying unrelated context.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_argument_assertion",
      "fixture_template": "Held tool calls covering valid, boundary, and invalid argument shapes."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never assume schema-valid arguments are semantically correct.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-strict-tool-use"
    }
  },
  {
    "id": "anthropic-output-budget",
    "family": "routing",
    "title": "Bound Anthropic output for narrow tasks",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "narrow task with output allowance far above observed need",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Set a workload-specific output ceiling and keep completion checks that fail when required content is cut.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held short and longest-valid responses judged for completeness under the new ceiling."
    },
    "experiment": {
      "method": "canary",
      "metric": "tokens_out"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower an output ceiling from averages alone; tail cases must still complete.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-anthropic-output-budget"
    }
  },
  {
    "id": "openai-prefix-ordering",
    "family": "cache",
    "title": "Put reusable OpenAI content first",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "long request with stable and variable sections",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Place stable instructions, examples, tools, and reference blocks before request-specific content so exact prefix matching can reuse them.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held requests with a stable prefix and changing suffix under an uncached-token ceiling."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never attribute an automatic OpenAI cache hit to Caveman on a per-request basis.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-prefix-ordering"
    }
  },
  {
    "id": "openai-prompt-cache-key",
    "family": "cache",
    "title": "Partition OpenAI cache affinity keys",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "repeated prefixes split across unrelated cache routing keys",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use a stable low-cardinality cache key per reusable prompt template, without embedding request identifiers or secrets.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held template requests sharing one safe key and bounded uncached input."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never describe routing affinity as a guaranteed or provider-causal cache hit.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-prompt-cache-key"
    }
  },
  {
    "id": "openai-explicit-cache-breakpoint",
    "family": "cache",
    "title": "Mark explicit OpenAI cache boundaries",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "GPT-5.6 request needing caller-controlled cache placement",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use explicit mode and mark the end of reusable content when automatic placement would write volatile or low-reuse prefixes.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held GPT-5.6 requests with explicit breakpoint and bounded cache-write tokens."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never enable explicit mode on unsupported models or claim causality before end-to-end provider proof.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-explicit-cache-breakpoint"
    }
  },
  {
    "id": "openai-cache-write-read-observability",
    "family": "cache",
    "title": "Measure OpenAI cache writes and reads",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "cache-eligible OpenAI traffic without cached and write token tracking",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Record provider-reported cached tokens and cache-write tokens before changing cache mode, boundaries, or retention.",
      "apply": "manual"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held response requiring cache usage fields to exist and contain non-negative values."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat cached token counters as Caveman-caused savings on automatic caching paths.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-cache-write-read-observability"
    }
  },
  {
    "id": "openai-batch-offload",
    "family": "routing",
    "title": "Batch non-interactive OpenAI work",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "bulk or scheduled requests with no interactive latency need",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Move independent background requests to Batch, preserve custom identifiers, and reconcile every success and error.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held synchronous and batch outputs normalized to the same expected task result."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never count queued work as completed or batch latency as acceptable without an explicit workload SLA.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-batch-offload"
    }
  },
  {
    "id": "openai-flex-processing",
    "family": "routing",
    "title": "Use Flex for delay-tolerant OpenAI work",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "non-production or asynchronous work tolerant of slower and variable service",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Select Flex only for workloads whose latency and availability contract permits it, with fallback and queue timing observed.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "latency_threshold",
      "fixture_template": "Held asynchronous workload with a declared completion-time ceiling."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never move interactive or availability-sensitive traffic to Flex from price alone.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-flex-processing"
    }
  },
  {
    "id": "reasoning-effort",
    "family": "routing",
    "title": "Tune OpenAI reasoning effort by workload",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "fixed high reasoning effort across easy and hard requests",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Evaluate the current effort and one lower setting on representative cases, then scope the lower setting to passing work.",
      "apply": "review_reasoning"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held easy, boundary, and hard cases judged by a different model family."
    },
    "experiment": {
      "method": "canary",
      "metric": "tokens_out"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never choose reasoning effort from task labels without outcome evaluation.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-reasoning-effort"
    }
  },
  {
    "id": "openai-persisted-reasoning",
    "family": "input_bloat",
    "title": "Reuse relevant OpenAI reasoning state",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "multi-turn task that re-derives stable prior reasoning",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Preserve supported reasoning items across related turns and stop carrying them when prior reasoning is no longer relevant.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held multi-turn task comparing continued and fresh reasoning paths for answer quality."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never reuse reasoning across unrelated tasks or assume reuse lowers total cost without measurement.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-persisted-reasoning"
    }
  },
  {
    "id": "openai-structured-output",
    "family": "reliability",
    "title": "Replace parse retries with strict OpenAI output",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "free-form response reparsed or retried into a known schema",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use strict structured output for stable schemas and retain semantic validation for fields the schema cannot prove.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "json_schema",
      "fixture_template": "Held outputs covering required, optional, boundary, and refused result shapes."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never equate schema validity with factual or business correctness.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openai-structured-output"
    }
  },
  {
    "id": "output-brevity",
    "family": "routing",
    "title": "Bound OpenAI output for narrow tasks",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "narrow task with output allowance far above observed need",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Set a workload-specific output ceiling and gate longest-valid cases for completeness.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held short and longest-valid responses judged for completeness under the new ceiling."
    },
    "experiment": {
      "method": "canary",
      "metric": "tokens_out"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower an output ceiling from median behavior alone.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-output-brevity"
    }
  },
  {
    "id": "gemini-prefix-ordering",
    "family": "cache",
    "title": "Put reusable Gemini content first",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "repeated long context with a stable prefix",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Place stable context before changing user content so implicit caching can match a longer prefix.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held GenerateContent requests with bounded non-cached input."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never attribute implicit Gemini cache hits to Caveman on a per-request basis.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-prefix-ordering"
    }
  },
  {
    "id": "gemini-cache-observability",
    "family": "cache",
    "title": "Measure Gemini cached token use",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "cache-enabled Gemini traffic without cached token accounting",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Record provider usage metadata for cached content, prompt, candidates, and thoughts before tuning caching.",
      "apply": "manual"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held response requiring cached-content and total token usage fields to exist."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never infer caused savings from implicit cached-token counters.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cache-observability"
    }
  },
  {
    "id": "gemini-batch-offload",
    "family": "routing",
    "title": "Batch non-interactive Gemini work",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "bulk or scheduled GenerateContent work with no interactive latency need",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Move independent background requests to Batch and reconcile each result or error before downstream use.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held synchronous and batch outputs normalized to the same expected task result."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat job submission as task completion or ignore per-item failures.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-batch-offload"
    }
  },
  {
    "id": "gemini-token-count-preflight",
    "family": "input_bloat",
    "title": "Preflight large Gemini requests with provider counts",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large multimodal or text request near a threshold",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Use countTokens for sampled or boundary requests before deciding whether to trim, split, or reject them.",
      "apply": "manual"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Held request whose provider token count must remain below the configured input ceiling."
    },
    "experiment": {
      "method": "shadow",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never price multimodal input with a local text tokenizer.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-token-count-preflight"
    }
  },
  {
    "id": "gemini-thinking-budget-right-sizing",
    "family": "routing",
    "title": "Tune Gemini thinking budget",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "fixed thinking budget across easy and hard requests",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Evaluate lower thinking settings on representative tasks and scope any change to workloads that preserve quality.",
      "apply": "review_reasoning"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held easy, boundary, and hard cases judged for correctness under each thinking setting."
    },
    "experiment": {
      "method": "canary",
      "metric": "tokens_out"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower thinking from visible answer length alone.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-thinking-budget-right-sizing"
    }
  },
  {
    "id": "gemini-flash-right-sizing",
    "family": "routing",
    "title": "Evaluate Gemini Flash for narrow work",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "Gemini Pro-class model serving narrow high-volume work",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Evaluate a compatible Flash candidate on held cases and canary only the passing workload.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held representative cases with candidate and baseline outputs judged outside their model family."
    },
    "experiment": {
      "method": "canary",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never infer parity from model-family naming or unit price.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-flash-right-sizing"
    }
  },
  {
    "id": "bedrock-cachepoint-placement",
    "family": "cache",
    "title": "Place Bedrock cache checkpoints",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "supported Bedrock model with repeated stable context",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Add supported cache checkpoints after stable tools, system content, or messages while respecting model-specific minimums and order.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Repeated held Converse or InvokeModel requests with bounded uncached input."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never mark Bedrock savings verified until Caveman proves the provider-causal path end to end.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-bedrock-cachepoint-placement"
    }
  },
  {
    "id": "bedrock-cache-observability",
    "family": "cache",
    "title": "Measure Bedrock cache reads and writes",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "Bedrock cache-enabled traffic without normalized cache counters",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Record cache read, cache write, lifetime detail, and non-cached input from provider responses before tuning checkpoints.",
      "apply": "manual"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held cached response requiring read, write, and non-cached usage fields."
    },
    "experiment": {
      "method": "trial",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never sum Bedrock input and cache counters without following the provider response contract.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-bedrock-cache-observability"
    }
  },
  {
    "id": "claude-code-claude-md-budget",
    "family": "input_bloat",
    "title": "Keep CLAUDE.md load-bearing and small",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "large CLAUDE.md loaded into every turn",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Keep durable project rules in CLAUDE.md and move procedures or long references into on-demand skills.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held repository tasks requiring each retained rule after the context edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove a rule without proving its behavior remains enforced or available on demand.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-claude-md-budget"
    }
  },
  {
    "id": "claude-code-scoped-rules",
    "family": "input_bloat",
    "title": "Scope Claude Code rules to relevant paths",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "repository-wide instructions relevant to one subtree",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Move subtree-specific guidance to the narrowest supported project location so unrelated work does not carry it.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held tasks inside and outside the target subtree verifying rule presence only where needed."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never narrow scope when the rule protects repository-wide correctness or safety.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-scoped-rules"
    }
  },
  {
    "id": "claude-code-skill-deferral",
    "family": "input_bloat",
    "title": "Load Claude Code procedures as skills",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "long workflow instructions always loaded but rarely used",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Move reusable procedures into focused skills whose bodies load only when invoked or matched.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held task that invokes the skill and an unrelated task that must not load its body."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat skill discovery metadata as free; keep names and descriptions concise.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-skill-deferral"
    }
  },
  {
    "id": "claude-code-explorer-subagent",
    "family": "input_bloat",
    "title": "Use isolated Claude Code exploration",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "broad repository reads polluting solver history",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Use a read-only isolated explorer for broad localization and return only compact evidence to the main solver.",
      "apply": "offload_exploration"
    },
    "grader": {
      "type": "localization_f1",
      "fixture_template": "Held repository localization tasks with expected files and line neighborhoods."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never delegate a one-file lookup or assume isolated work lowers combined tokens.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-explorer-subagent"
    }
  },
  {
    "id": "claude-code-command-output-hook",
    "family": "input_bloat",
    "title": "Rewrite noisy Claude Code commands through shrink",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "shell command likely to return large repetitive output",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Use the verified PreToolUse rewrite to route eligible shell commands through recoverable output shrinking.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held command outputs whose compact view and byte-exact recovery both match expected content."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never rewrite commands outside the verified hook contract or lose access to original output.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-command-output-hook"
    }
  },
  {
    "id": "claude-code-subagent-model-right-sizing",
    "family": "routing",
    "title": "Right-size Claude Code subagent models",
    "predicate": {
      "harness": [
        "claude-code"
      ],
      "payload_shape": "read-only or narrow subagent using the main solver model",
      "wire_protocol": [
        "anthropic"
      ]
    },
    "fix": {
      "prose": "Assign a cheaper compatible model to narrow subagents and gate the combined workflow on final task quality.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "localization_f1",
      "fixture_template": "Held subagent tasks with expected evidence and final solver outcomes."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never count main-agent savings without including subagent tokens and calls.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-claude-code-subagent-model-right-sizing"
    }
  },
  {
    "id": "codex-agents-md-budget",
    "family": "input_bloat",
    "title": "Keep AGENTS.md concise",
    "predicate": {
      "harness": [
        "codex"
      ],
      "payload_shape": "large AGENTS.md guidance loaded into Codex turns",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Keep durable rules in AGENTS.md and move repeatable procedures or references into on-demand skills.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held repository tasks requiring each retained rule after the context edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never trim instructions whose absence changes required scope, safety, or verification behavior.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-codex-agents-md-budget"
    }
  },
  {
    "id": "codex-scoped-agents",
    "family": "input_bloat",
    "title": "Scope Codex instructions by directory",
    "predicate": {
      "harness": [
        "codex"
      ],
      "payload_shape": "root instructions relevant only to a nested path",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Move path-specific rules into the nearest applicable AGENTS.md while preserving parent precedence.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held tasks in sibling paths verifying only applicable instructions are active."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never move a repository-wide rule into a subtree.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-codex-scoped-agents"
    }
  },
  {
    "id": "codex-skill-deferral",
    "family": "input_bloat",
    "title": "Load Codex procedures as skills",
    "predicate": {
      "harness": [
        "codex"
      ],
      "payload_shape": "long repeatable workflow embedded in always-loaded instructions",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Move the workflow into a focused SKILL.md and keep only concise triggering guidance in persistent instructions.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held matching and unrelated tasks verifying skill activation and non-activation."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never create a skill whose discovery description costs more than the procedure it replaces.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-codex-skill-deferral"
    }
  },
  {
    "id": "codex-command-output-nudge",
    "family": "input_bloat",
    "title": "Prefer shrink for noisy Codex commands",
    "predicate": {
      "harness": [
        "codex"
      ],
      "payload_shape": "shell commands producing large repetitive output",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Install the reversible AGENTS.md instruction note because Codex lacks a verified byte-safe hard rewrite surface for this path.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Held noisy command task with an input-token ceiling and byte-exact recovery check."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never describe this instruction note as a deterministic command rewrite.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-codex-command-output-nudge"
    }
  },
  {
    "id": "codex-compaction-threshold",
    "family": "input_bloat",
    "title": "Tune Codex auto-compaction threshold",
    "predicate": {
      "harness": [
        "codex"
      ],
      "payload_shape": "long sessions compacting too late or repeatedly",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Set a threshold from the model window and observed session shape, then verify required state survives compaction.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long session requiring earlier decisions and identifiers after compaction."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never tune compaction from token reduction alone; state retention must pass.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-codex-compaction-threshold"
    }
  },
  {
    "id": "gemini-cli-gemini-md-budget",
    "family": "input_bloat",
    "title": "Keep GEMINI.md concise",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "large GEMINI.md hierarchy loaded into every turn",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Keep durable project rules in context files and move long procedures into on-demand skills.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held repository tasks requiring each retained rule after the context edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove project guidance without proving required behavior remains.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-gemini-md-budget"
    }
  },
  {
    "id": "gemini-cli-context-imports",
    "family": "input_bloat",
    "title": "Modularize Gemini CLI context",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "monolithic context file mixing unrelated project guidance",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Split context into stable modules and import only modules that remain globally load-bearing.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held tasks requiring each imported module and unrelated tasks checking omitted modules."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Imports improve maintainability, not tokens, unless the loaded set becomes smaller.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-context-imports"
    }
  },
  {
    "id": "gemini-cli-skill-deferral",
    "family": "input_bloat",
    "title": "Load Gemini CLI procedures as skills",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "rare workflow stored in always-loaded context",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Move reusable procedures into Agent Skills and keep persistent context focused on rules needed every turn.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held matching and unrelated tasks verifying skill activation and non-activation."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never count deferred skill bodies as removed if their discovery metadata still dominates context.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-skill-deferral"
    }
  },
  {
    "id": "gemini-cli-explorer-subagent",
    "family": "input_bloat",
    "title": "Use Gemini CLI isolated exploration",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "broad investigation generating large read-only context",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Delegate broad codebase investigation to an isolated read-only subagent and return compact evidence.",
      "apply": "offload_exploration"
    },
    "grader": {
      "type": "localization_f1",
      "fixture_template": "Held repository localization tasks with expected files and line neighborhoods."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never delegate small lookups or omit combined subagent usage from measurement.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-explorer-subagent"
    }
  },
  {
    "id": "gemini-cli-command-output-hook",
    "family": "input_bloat",
    "title": "Rewrite noisy Gemini CLI commands through shrink",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "run_shell_command likely to return large repetitive output",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Use the verified BeforeTool rewrite to route eligible shell commands through recoverable output shrinking.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held command outputs whose compact view and byte-exact recovery match expected content."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never emit non-JSON hook stdout or rewrite unsupported tool inputs.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-command-output-hook"
    }
  },
  {
    "id": "gemini-cli-api-key-token-cache",
    "family": "cache",
    "title": "Preserve Gemini CLI token caching eligibility",
    "predicate": {
      "harness": [
        "gemini"
      ],
      "payload_shape": "Gemini CLI session using an authentication mode without cached-content support",
      "wire_protocol": [
        "gemini"
      ]
    },
    "fix": {
      "prose": "Use a supported API-key or Vertex authentication path when token caching matters and policy permits it.",
      "apply": "manual"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held CLI session requiring cached-token usage to appear in stats under the selected authentication mode."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never change authentication mode solely for cache savings without security and account-policy approval.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-gemini-cli-api-key-token-cache"
    }
  },
  {
    "id": "opencode-instruction-budget",
    "family": "input_bloat",
    "title": "Keep OpenCode instructions concise",
    "predicate": {
      "harness": [
        "opencode"
      ],
      "payload_shape": "large always-loaded project instructions",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Keep persistent rules short and move task procedures into focused commands, skills, or agents.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held project tasks requiring retained rules after the instruction edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove instructions that define required repository behavior.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-opencode-instruction-budget"
    }
  },
  {
    "id": "opencode-tool-permission-pruning",
    "family": "input_bloat",
    "title": "Restrict OpenCode agent tools",
    "predicate": {
      "harness": [
        "opencode"
      ],
      "payload_shape": "specialized agent receiving unrelated tool schemas",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use agent-specific permissions to expose only tools required for the role.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_not_called",
      "fixture_template": "Held specialized tasks verifying denied tools remain unavailable and required tools still work."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never prune a tool from the main agent without a reliable route to regain it when needed.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-opencode-tool-permission-pruning"
    }
  },
  {
    "id": "opencode-cheap-subagent-model",
    "family": "routing",
    "title": "Right-size OpenCode subagent models",
    "predicate": {
      "harness": [
        "opencode"
      ],
      "payload_shape": "narrow plan or read-only agent using the main model",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Assign a cheaper compatible model to the narrow agent and gate the combined workflow on final task quality.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held delegated tasks comparing final outcomes with baseline and candidate subagent models."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never omit parent-agent and delegated-call usage from the comparison.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-opencode-cheap-subagent-model"
    }
  },
  {
    "id": "opencode-command-output-plugin",
    "family": "input_bloat",
    "title": "Rewrite noisy OpenCode commands through shrink",
    "predicate": {
      "harness": [
        "opencode"
      ],
      "payload_shape": "bash tool command likely to return large repetitive output",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use the verified plugin hook to route eligible commands through recoverable output shrinking.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held command outputs whose compact view and byte-exact recovery match expected content."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never claim hard rewrite support outside the tested plugin event.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-opencode-command-output-plugin"
    }
  },
  {
    "id": "opencode-mcp-tool-pruning",
    "family": "input_bloat",
    "title": "Hide unused OpenCode MCP tools",
    "predicate": {
      "harness": [
        "opencode"
      ],
      "payload_shape": "large MCP tool surface irrelevant to one agent",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Deny unrelated MCP tool groups on specialized agents and keep an explicit route for tasks that need them.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_not_called",
      "fixture_template": "Held specialized tasks verifying hidden MCP tools stay unavailable and required tools remain callable."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never hide tools from workflows that lack another discovery or delegation path.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-opencode-mcp-tool-pruning"
    }
  },
  {
    "id": "hermes-context-file-budget",
    "family": "input_bloat",
    "title": "Bound Hermes project context files",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "large project context files loaded into every session",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Keep always-loaded project facts concise and move procedures into on-demand skills.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held project tasks requiring retained context after the edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower context limits to hide truncation of load-bearing instructions.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-context-file-budget"
    }
  },
  {
    "id": "hermes-context-compression-tuning",
    "family": "input_bloat",
    "title": "Tune Hermes context compression",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "sessions compressing too early, too late, or too often",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Tune compression threshold, retained tail, and protected messages from observed sessions, then gate state retention.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long sessions requiring prior decisions, identifiers, and current task state after compression."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never optimize only for fewer tokens when compression drops task state.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-context-compression-tuning"
    }
  },
  {
    "id": "hermes-compression-model-right-sizing",
    "family": "routing",
    "title": "Right-size Hermes compression model",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "context summarization using the primary solver model",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Evaluate a cheaper auxiliary model for compression and keep it only when state-retention checks pass.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long sessions comparing retained facts and task completion across compression models."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never count cheaper summary calls when they cause more solver turns or lost state.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-compression-model-right-sizing"
    }
  },
  {
    "id": "hermes-file-read-dedup",
    "family": "input_bloat",
    "title": "Preserve Hermes file-read deduplication",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "same unchanged file region read repeatedly",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Keep built-in read deduplication enabled and avoid workflows that re-inline unchanged regions after lightweight stubs are available.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "token_threshold",
      "fixture_template": "Held repeated read of one unchanged region with an input-token ceiling on later turns."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never deduplicate a region after its file content changes.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-file-read-dedup"
    }
  },
  {
    "id": "hermes-skill-deferral",
    "family": "input_bloat",
    "title": "Keep Hermes procedures in on-demand skills",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "long procedural guidance loaded outside its task",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use progressive-disclosure skills for procedures and keep persistent memory for short durable facts.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held matching and unrelated tasks verifying skill activation and non-activation."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never let skill discovery metadata grow into another always-loaded manual.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-skill-deferral"
    }
  },
  {
    "id": "hermes-toolset-pruning",
    "family": "input_bloat",
    "title": "Select only needed Hermes toolsets",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "session or delegate exposed to unrelated toolsets",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Enable only toolsets required by the platform and task, with a separate route for broader work.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_not_called",
      "fixture_template": "Held narrow tasks proving hidden tools stay unused and required tools remain available."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never prune a toolset without checking all task paths that depend on it.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-toolset-pruning"
    }
  },
  {
    "id": "hermes-command-output-plugin",
    "family": "input_bloat",
    "title": "Rewrite noisy Hermes commands through shrink",
    "predicate": {
      "harness": [
        "hermes"
      ],
      "payload_shape": "terminal tool output likely to be large and repetitive",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use the verified Hermes plugin path to shrink eligible output while retaining byte-exact recovery.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held command outputs whose compact view and recovery match expected content."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never discard original output or claim rewrite support outside the tested plugin path.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-hermes-command-output-plugin"
    }
  },
  {
    "id": "openclaw-bootstrap-budget",
    "family": "input_bloat",
    "title": "Bound OpenClaw bootstrap context",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "large workspace bootstrap files injected every run",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Trim bootstrap files to load-bearing facts and move procedures into on-demand skills or memory.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held workspace tasks requiring retained bootstrap rules after the edit."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower bootstrap caps to silently truncate required instructions.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-bootstrap-budget"
    }
  },
  {
    "id": "openclaw-tool-schema-pruning",
    "family": "input_bloat",
    "title": "Reduce OpenClaw tool schema surface",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "tool schema JSON dominates the context breakdown",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Expose only tools needed by each agent and use separate agents or skills for broader capabilities.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_not_called",
      "fixture_template": "Held narrow tasks proving hidden tools stay unavailable and required tools remain callable."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove a tool without a tested route for tasks that need it.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-tool-schema-pruning"
    }
  },
  {
    "id": "openclaw-session-pruning",
    "family": "input_bloat",
    "title": "Prune stale OpenClaw tool results",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "long session retaining old large tool results",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Enable session pruning for old tool results while preserving the persisted transcript and current task evidence.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long session requiring recent and previously extracted facts after pruning."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never prune a tool result still needed for current task correctness.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-session-pruning"
    }
  },
  {
    "id": "openclaw-compaction-tail-budget",
    "family": "input_bloat",
    "title": "Tune OpenClaw compaction tail",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "long sessions retaining too much or too little recent history",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Tune recent-token and reserve budgets from observed sessions, preserving tool-call pairs and current task state.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long session requiring recent tool linkage, identifiers, and earlier decisions after compaction."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never reduce the retained tail without proving current task continuity.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-compaction-tail-budget"
    }
  },
  {
    "id": "openclaw-compaction-model-right-sizing",
    "family": "routing",
    "title": "Right-size OpenClaw compaction model",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "compaction or memory flush using the primary conversation model",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Evaluate a cheaper exact model override for housekeeping and retain it only when state checks pass.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held long sessions comparing retained facts and task completion across compaction models."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never accept cheaper housekeeping when it causes lost state or paid fallback calls.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-compaction-model-right-sizing"
    }
  },
  {
    "id": "openclaw-lightweight-subagent-context",
    "family": "input_bloat",
    "title": "Use lightweight OpenClaw subagent context",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "isolated subagent receiving unnecessary parent context state",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use isolated lightweight context for self-contained delegated work and return only the result needed by the parent.",
      "apply": "offload_exploration"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held delegated tasks comparing required result fields under full and lightweight context."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never use lightweight context when the child depends on parent decisions or hidden state.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-lightweight-subagent-context"
    }
  },
  {
    "id": "openclaw-command-output-plugin",
    "family": "input_bloat",
    "title": "Rewrite noisy OpenClaw commands through shrink",
    "predicate": {
      "harness": [
        "openclaw"
      ],
      "payload_shape": "tool execution output likely to be large and repetitive",
      "wire_protocol": [
        "openai"
      ]
    },
    "fix": {
      "prose": "Use the verified plugin interception path to shrink eligible output while retaining byte-exact recovery.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held command outputs whose compact view and recovery match expected content."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never claim command rewrite support outside the tested OpenClaw plugin path.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-openclaw-command-output-plugin"
    }
  },
  {
    "id": "stable-map-serialization",
    "family": "cache",
    "title": "Serialize stable maps deterministically",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "semantically identical prefix with changing object key order",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Sort keys and use deterministic serialization for cacheable request sections without changing their semantics.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held objects with varied insertion order that must serialize identically and produce equivalent outputs."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never normalize fields whose order is semantically meaningful.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-stable-map-serialization"
    }
  },
  {
    "id": "volatile-prefix-isolation",
    "family": "cache",
    "title": "Move volatile fields after stable prefixes",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "timestamp, request identifier, nonce, or live state inside reusable prefix",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Move volatile fields after the cache boundary or omit them when the model does not need them.",
      "apply": "stabilize_prefix"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held requests varying volatile fields while retaining equivalent task outputs and stable prefix bytes."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "cache_hit_ratio"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never remove live state that affects model correctness merely to improve cache reuse.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-volatile-prefix-isolation"
    }
  },
  {
    "id": "prompt-literal-dedup",
    "family": "input_bloat",
    "title": "Remove duplicated prompt instructions",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "same instruction or example repeated inside one request",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "State each load-bearing instruction once and remove duplicated literals from composed prompt sections.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held cases requiring every unique instruction after duplicate removal."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never merge repetitions that intentionally reinforce conflicting or high-risk constraints without an eval.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-prompt-literal-dedup"
    }
  },
  {
    "id": "few-shot-example-pruning",
    "family": "input_bloat",
    "title": "Keep only useful few-shot examples",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large example set with redundant or non-discriminating cases",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Remove one example group at a time and retain only examples that protect a measured behavior.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held cases mapped to each retained example behavior plus unseen boundary cases."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never prune examples without measuring the behavior each example protects.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-few-shot-example-pruning"
    }
  },
  {
    "id": "lost-in-middle-context-ordering",
    "family": "input_bloat",
    "title": "Keep critical evidence out of the context middle",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "long multi-document context with critical evidence buried centrally",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Retrieve and place the most relevant evidence near the query or another tested high-attention position.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held retrieval tasks with required facts placed at beginning, middle, and end."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat one universal position as optimal across models and task shapes.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-lost-in-middle-context-ordering"
    }
  },
  {
    "id": "retrieval-top-k-tuning",
    "family": "input_bloat",
    "title": "Tune retrieval depth on held queries",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "retriever returns a fixed large candidate set for every query",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Select retrieval depth against held recall and task quality instead of filling the available window.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "localization_f1",
      "fixture_template": "Held queries with expected supporting documents or code locations across candidate depths."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never lower retrieval depth from token count alone when support recall falls.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-retrieval-top-k-tuning"
    }
  },
  {
    "id": "retrieval-chunk-sizing",
    "family": "input_bloat",
    "title": "Tune retrieval chunk size",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "retrieved chunks contain large irrelevant neighborhoods or split required facts",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Evaluate chunk size and overlap against held support recall and total injected tokens.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "localization_f1",
      "fixture_template": "Held queries whose supporting spans cross normal and boundary chunk positions."
    },
    "experiment": {
      "method": "local_ab",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never select chunk size from corpus averages without measuring retrieval and answer quality.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-retrieval-chunk-sizing"
    }
  },
  {
    "id": "exact-response-cache",
    "family": "cache",
    "title": "Cache exact deterministic responses",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "identical deterministic requests repeated with stable dependencies",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Cache responses by exact normalized request and dependency version, with explicit expiry and invalidation.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held repeated requests plus changed dependency and expiry cases."
    },
    "experiment": {
      "method": "shadow",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never serve cached output when user, tool, policy, or source state can change the answer.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-exact-response-cache"
    }
  },
  {
    "id": "semantic-response-cache",
    "family": "cache",
    "title": "Gate semantic response caching",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "near-duplicate requests in a context-stable workload",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Use a context-aware similarity gate, explicit tenancy and version keys, and a false-hit eval before any canary.",
      "apply": "enable_gateway_optimizer"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held similar and adversarially close requests with fresh and cached outputs judged for interchangeability."
    },
    "experiment": {
      "method": "canary",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat embedding similarity as proof that two answers are interchangeable.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-semantic-response-cache"
    }
  },
  {
    "id": "retry-request-deduplication",
    "family": "reliability",
    "title": "Deduplicate in-flight request retries",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "same logical request replayed concurrently after timeout",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Coalesce identical in-flight work under a scoped request key and return the completed result to waiters.",
      "apply": "review_retries"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held concurrent duplicate calls requiring one provider execution and identical waiter results."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never deduplicate requests whose authorization, tool state, or side effects differ.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-retry-request-deduplication"
    }
  },
  {
    "id": "retry-after-backoff",
    "family": "reliability",
    "title": "Honor retry timing and add jitter",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "rate-limit or transient retry loop without server timing or jitter",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Treat server retry timing as a floor, add bounded exponential backoff and jitter, and retry only recoverable classes.",
      "apply": "review_retries"
    },
    "grader": {
      "type": "latency_threshold",
      "fixture_template": "Held transient failures with declared minimum delay, bounded completion time, and no retry on permanent errors."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never retry permanent client errors or ignore provider timing guidance.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-retry-after-backoff"
    }
  },
  {
    "id": "retry-attempt-cap",
    "family": "reliability",
    "title": "Cap repeated provider attempts",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "retry policy with no maximum attempt or elapsed-time bound",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Set workload-specific attempt and elapsed-time caps, then surface terminal failure instead of looping.",
      "apply": "review_retries"
    },
    "grader": {
      "type": "http_status",
      "fixture_template": "Held persistent failure requiring bounded attempts and the expected terminal error."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never hide terminal failure behind synthetic success after the cap.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-retry-attempt-cap"
    }
  },
  {
    "id": "provider-circuit-breaker",
    "family": "reliability",
    "title": "Open a circuit on sustained provider failure",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "repeated transient failures continuing across requests",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Stop calls after a measured failure threshold, probe recovery conservatively, and use only approved fallback behavior.",
      "apply": "review_retries"
    },
    "grader": {
      "type": "http_status",
      "fixture_template": "Held failure burst, open interval, probe, and recovery sequence with expected statuses."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never return a fabricated model result while the circuit is open.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-provider-circuit-breaker"
    }
  },
  {
    "id": "tool-result-pagination",
    "family": "input_bloat",
    "title": "Page large tool results",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "large list or document returned in one tool result",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Return a bounded first page plus a stable continuation handle and fetch later pages only when needed.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "tool_sequence",
      "fixture_template": "Held tasks requiring first-page facts, later-page facts, and no unnecessary continuation."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never truncate silently; expose continuation and preserve result order.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-tool-result-pagination"
    }
  },
  {
    "id": "tool-result-field-projection",
    "family": "input_bloat",
    "title": "Project only needed tool fields",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "wide structured tool result with sparsely used fields",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Select task-relevant fields before model injection and retain a handle for omitted data.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "json_path_assertion",
      "fixture_template": "Held tasks requiring each retained field and one omitted field recovered through the handle."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never drop fields without a tested recovery path for tasks that need them.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-tool-result-field-projection"
    }
  },
  {
    "id": "document-summary-memoization",
    "family": "input_bloat",
    "title": "Reuse document summaries",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "same versioned document summarized repeatedly",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Memoize summaries by document content hash, prompt version, and model configuration, then invalidate on any dependency change.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "exact_match",
      "fixture_template": "Held unchanged, changed-content, changed-prompt, and changed-model cases."
    },
    "experiment": {
      "method": "replay",
      "metric": "tokens_in"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never reuse a summary after its document or summarization contract changes.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-document-summary-memoization"
    }
  },
  {
    "id": "structured-output-retry-elimination",
    "family": "reliability",
    "title": "Remove schema-repair re-asks",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "invalid structured output repaired by another full model request",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Use provider-native strict output where available, validate once, and handle refusal or invalid results without blind full-context re-asks.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "json_schema",
      "fixture_template": "Held valid, boundary, refusal, and invalid outputs against the required schema."
    },
    "experiment": {
      "method": "replay",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never treat schema conformance as semantic correctness.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-structured-output-retry-elimination"
    }
  },
  {
    "id": "output-length-contract",
    "family": "routing",
    "title": "Set explicit output length contracts",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "verbose free-form output where a bounded structure satisfies the task",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Specify the required structure and concise length, then gate completeness and boundary cases.",
      "apply": "enable_sdk_config"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held tasks covering minimum, typical, and longest-valid complete answers."
    },
    "experiment": {
      "method": "canary",
      "metric": "tokens_out"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never call shorter output better when required content is missing.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-output-length-contract"
    }
  },
  {
    "id": "model-cascade",
    "family": "routing",
    "title": "Escalate only hard requests",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "mixed-difficulty workload always routed to the strongest model",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Run a cheaper candidate first and escalate only under a calibrated uncertainty or validation rule.",
      "apply": "right_size_model"
    },
    "grader": {
      "type": "llm_judge",
      "fixture_template": "Held easy, ambiguous, and hard cases testing answer quality and escalation decisions."
    },
    "experiment": {
      "method": "canary",
      "metric": "$/day band"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never count cascade savings without including escalated requests and failed first attempts.",
    "skill_render": {
      "eligible": true,
      "skill_name": "caveman-practice-model-cascade"
    }
  },
  {
    "id": "subagent-fanout-guard",
    "family": "cache",
    "title": "Bound sub-agent fan-out with operator-approved limits",
    "predicate": {
      "harness": [
        "any"
      ],
      "payload_shape": "concurrent sibling sub-agent spans sharing one parent and a large request prefix without cache reuse",
      "wire_protocol": [
        "any"
      ]
    },
    "fix": {
      "prose": "Review the framework callsite and propose explicit root and child limits selected by an operator only after paired outcome replay.",
      "apply": "add_subagent_guard"
    },
    "grader": {
      "type": "tool_sequence",
      "fixture_template": "Paired held tasks asserting the guarded arm preserves required child results and failure handling under the operator-selected cap."
    },
    "experiment": {
      "method": "replay",
      "metric": "task_outcome_pass_rate"
    },
    "evidence": {
      "status": "unmeasured"
    },
    "never_claim": "Never derive a cap from sibling count or recorded spend, and never present the proposal, merge, or active guard as savings; this report-only recipe mints nothing.",
    "skill_render": {
      "eligible": false,
      "skill_name": "caveman-recipe-subagent-fanout-guard"
    }
  }
] as const;
