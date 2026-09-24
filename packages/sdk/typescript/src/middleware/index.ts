/**
 * `@caveman-ai/sdk/middleware`: the protocol client that `@caveman-ai/middleware` adapters build on.
 *
 * @experimental Every export of this subpath may change in any minor release while the middleware packages are 0.x.
 * @packageDocumentation
 */
export * from './types.js';
export * from './runtime.js';
export * from './protocol.js';
export { MiddlewareError, parseCapabilities, sha256, scopeKey, validatePlan } from './validate.js';
export type { PlanCapabilities } from './validate.js';
