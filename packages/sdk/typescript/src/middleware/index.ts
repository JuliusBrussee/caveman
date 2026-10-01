/**
 * `@caveman-ai/sdk/middleware`: the protocol client that `@caveman-ai/middleware` adapters build on.
 *
 * @packageDocumentation
 */
export * from './types.js';
export * from './runtime.js';
export * from './protocol.js';
export { MiddlewareError, parseCapabilities, sha256, scopeKey, validatePlan } from './validate.js';
export type { PlanCapabilities } from './validate.js';
