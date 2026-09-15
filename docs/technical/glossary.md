# Glossary

## Accessibility tree

Browser-provided semantic representation of page roles, names, states, and
relationships. Caveman browser bridge uses it as compact alternative to full
page markup when visual pixels are unnecessary.

## Agent profile

Declarative description of how CLI launches and configures supported agent,
including wire protocol and endpoint injection plus any hooks, skills or plugins.

## Basis

Label explaining how a number was produced, such as inferred,
provider-reported, benchmark counterfactual, or verified.

## Byte-safe

Model-visible bytes remain unchanged, as in record mode. Compression, TOON,
pixel, and rewriting are not byte-safe.

## Cave Plan

Connected product report ranking possible efficiency improvements. A plan item
is a proposal and does not establish verified savings.

## Cave Score

Connected product score summarizing measured efficiency signals under its
published contract. It is distinct from local Engine token reduction.

## Caveman Cloud

Optional connected account, evidence, and governance service. Local compression
does not require it.

## CCR

Caveman Context Recovery. Content-addressed store mapping compact references to
exact original bytes or typed objects.

## Compressor

Engine component that transforms one recognized input shape into smaller
model-visible representation under declared policy.

## Context pack

SDK structure for assembling bounded context parts with provenance and policy.

## Fail closed

Unknown or invalid state rejects operation or produces safe non-success result.
For transforms, safe result is often original input.

## Headroom

Modeled opportunity to reduce context or cost. It is inferred until stronger
evidence exists.

## Hook

Code invoked by agent lifecycle event, such as session start, prompt submission,
or pre-tool execution.

## Inferred

Estimated by local model, tokenizer, or assumptions rather than confirmed by
provider or verification method.

## List-price subtotal

Usage units multiplied by dated public catalog price. It is not provider
invoice.

## MCP

Model Context Protocol. Caveman uses local standard-I/O server to expose
compression, recovery, stats, and TOON operations to compatible agents.

## Observed

Recorded before/after relationship without enough control to establish cause.

## Pixel

Lossy text-to-PNG context encoding for explicitly allowed vision-capable models.

## Provider-reported

Usage or event data returned by model provider response.

## Record mode

Pass-through mode that observes local traffic without changing model-visible
request bytes.

## Recovery handle

`ccr_...` content-derived identifier used to retrieve exact original bytes.

## Safety class

Category describing transformation risk. Current Engine compressors use S4,
lossy with recovery.

## Skill

Instruction package that changes model behavior or response style. It is not
executable runtime unless it includes separately reviewed hooks or tools.

## SSRF

Server-Side Request Forgery. Risk where configurable outbound URL lets caller
reach unintended local or private service.

## TOON

Token-Oriented Object Notation. Compact text encoding for suitable structured
data. It changes model-visible bytes and is never used for tool-call arguments.

## Unpriced

Marker stating catalog has no supported public price. Numeric fallback is zero
to avoid invented cost, but resource is not assumed free.

## Verified

Evidence basis reserved for values meeting named enforced verification method.
Local compression estimate is not verified by itself.

## Wire protocol

Provider request and response shape used between agent and proxy, such as
Anthropic Messages, OpenAI Responses, OpenAI Chat Completions, or Gemini
GenerateContent.
