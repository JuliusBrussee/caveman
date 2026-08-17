# Caveman 2 is here. The skill is untouched and still MIT.

Let me say the most important thing first, before anything else.

**The skill you starred is untouched. It is still MIT. It always will be.**

If you came here for `/caveman` — the thing that teaches your coding agent to talk like a caveman and cut its output tokens — nothing has changed for you. Same install, same license, same repo. We did not relicense it. We did not move it. We did not bolt a paywall onto it. 73,000 of you trusted us with a star on a free thing, and that free thing stays free, open, and yours.

Now that that's clear, here's what's new.

## What Caveman 2 is

Caveman v1 shrank the model's **mouth**. It made the model *talk* shorter through caveman voice. No output-reduction percentage is published until a reproducible run and raw outputs are committed.

Caveman 2 adds the **ears**. It shrinks what the model *reads*.

> Caveman make mouth smaller. Now Caveman make ears smaller too. 🪨

Most of your token bill isn't the model talking. It's the model listening — tool outputs, logs, build dumps, giant JSON blobs, search results, code you paste in for context. All of that hits the model at full size, and you pay for every token of it.

Caveman 2 is a real compression **engine**, plus a **proxy** and **SDKs**, that squeezes that input *before* it ever reaches the model. One line in front of any LLM app. Your keys. Your machine. Compression happens locally, and your requests still go only where they already went: the provider you configured. Nothing goes to Caveman's servers.

Here's a real run:

```text
$ caveman compress < orders.json
16,098 tokens  →  1,091 tokens     93% smaller  ·  byte-safe  ·  fully recoverable
```

That's a 40-record JSON blob going from 16,098 tokens to 1,091. The savings here are **inferred** — this is what you *could* save, measured on your own machine, not a number we projected or a number we claim is verified. (More on that word below; it matters.) And nothing is destroyed. The model can pull the full payload back on demand through CCR if it actually needs the detail. Byte-safe by default: on any parse hiccup, your bytes pass through untouched.

It's free. It's local. It's BYOK. It works in front of OpenAI, Anthropic, Bedrock, Vertex, Azure, OpenRouter, and the agent CLIs you already run.

## The license, said plainly

Here's the part where most companies get vague. We won't.

Caveman 2 ships under **two** licenses, and the split is deliberate:

**MIT — the funnel.** The skill, the CLI, both SDKs, the kit, the evals, the contracts, the provider-catalog, and the extension shell. Take it. Fork it. Embed it in whatever you want. This is the on-ramp and we want it everywhere.

**BSL-1.1 — Engine-linked runtime, including new modules.** Compression Engine, Proxy, Cache Engine, rewriter, Browse, MCP server, `shrink`, cavemem Go core, and shared platform. You can self-host all of it for your **own** traffic — including production — completely free. You can integrate it. You can read every line of source. Third-party hosted, managed, or embedded service use requires commercial license.

That's the whole restriction. One threat. Someone standing up "compression-as-a-service for other people's traffic" off the back of our work.

And here's the part that makes it honest: **BSL sunsets to Apache-2.0.** In about four years, the engine becomes fully permissive open source, automatically, no take-backs.

I know what some of you are thinking, because I'd think it too: *"open-washing."* So let me address it straight, no spin.

A BSL engine is not "fully open" today, and I'm not going to pretend it is. What it *is*: source-available, free to self-host for your real workloads, free to integrate, and on a clock to become Apache. We picked the most permissive license that blocks the one thing that would let someone rebuild our business out of our own code before we've built it. If you self-host Caveman to compress your company's own traffic, you will never see a bill and you never owe us anything. That's not a loophole. That's the design. The honesty is the brand here — I'm not going to start by lying about the license.

## Free vs Cloud, and why there's no bait-and-switch

The free engine gives you **inferred** savings. It tells you, on your own machine, *"you could save this much."* That number is honest, it's reproducible, and it's genuinely useful. The free tier is not a crippled demo. It's the actual engine.

Caveman **Cloud** gives you **verified** savings. The word "verified" is earned, not assumed. It means the optimizer ran on your *real* traffic, behind an eval gate, and proved your answer quality held while it cut your tokens. That's a different thing from an estimate, and it's the thing we charge for.

So the model is simple: **we give away the tool, we sell the proof.**

You self-host the engine free, forever, for your own traffic. If you want someone to *prove* the savings are real and that quality didn't slip — with eval gates, dashboards, signed metering, and a quality guardrail that auto-rolls-back when something regresses — that's Cloud, and that's what you pay for. The free `inferred` number is what makes you curious enough to want it verified. That's the whole funnel. There's no moment where the free thing gets worse to push you to pay. The free thing is complete.

## What ships now, what's coming

Landing as the v2 packages resolve:

- **Engine** — the compressors (JSON, logs, code, tool schemas) + CCR recovery
- **Proxy** — one base-URL line in front of any LLM app, byte-safe, measured
- **CLI** — `caveman compress`, `caveman wrap claude`, `caveman start`
- **SDKs** — Python and TypeScript, zero runtime deps, mirror each other

**Coming soon:** the browser extension (compress-before-send on ChatGPT / Claude / Gemini). It needs a fresh WASM build of the engine before it's ready, and we'd rather ship it right than ship it broken. Watch the repo to get pinged when it drops.

The install commands and the pip/npm/docker badges go live the moment those packages actually publish — not before. If you see them, they work.

## Help us build it — good first issue

The single highest-leverage thing you can contribute is a **new content-type compressor.**

The engine already squeezes JSON, logs, source code, and tool schemas. There are dozens more shapes of data that hit models all day and compress beautifully: CSV, HTML, SQL result sets, OpenAPI specs, GraphQL responses, Terraform plans, Kubernetes manifests, protobuf, stack traces from languages we don't cover yet. Each one is a self-contained win. You write the compressor, the engine routes to it, everybody who works with that data type saves tokens.

Look for the `good first issue` label. New compressors are the most fun and the most useful place to start.

## Links

- **Docs** → [caveman.so/docs](https://caveman.so/docs)
- **How we count** → [caveman.so/docs/methodology](https://caveman.so/docs/methodology)
- **Licensing detail** → [LICENSING.md](./LICENSING.md)
- **Cloud (verified savings)** → [caveman.so](https://caveman.so)

Thanks for using the skill. v2 exists to earn the same trust at engine scale.

🪨 few token. big save. no lie.

---

## GitHub Release notes (v2.0.0)

**Caveman 2 — the engine ships.** v1 shrank the model's mouth (output). v2 adds the ears: a real compression engine that squeezes what the model *reads* — tool outputs, logs, code, JSON — before it ever hits the model. One line in front of any LLM app. Free, local, BYOK.

**The skill is untouched and still MIT.** Nothing about your `/caveman` install changed.

Real run: a 40-record JSON went `16,098 → 1,091 tokens` (93% smaller), byte-safe, fully recoverable via CCR. Savings are **inferred** — measured on your machine, not projected, not "verified."

**Licensing:** MIT for skill, CLI, SDKs, kit, evals, contracts, catalog, and extension shell. BSL-1.1 for Engine/Proxy/Cache Engine/rewriter/Browse/MCP/shrink/cavemem-core/platform — new Engine-linked runtime modules default to BSL-1.1; self-host own traffic (incl. production) free; third-party hosted/managed/embed use needs commercial license. Sunsets to Apache-2.0 on scheduled Change Date.

**Ships now:** engine, proxy, CLI, SDKs (as the packages resolve). **Coming soon:** browser extension. Install commands and pip/npm/docker badges go live when the packages publish.

Want to help? New content-type compressors are the highest-leverage contribution — check `good first issue`.
