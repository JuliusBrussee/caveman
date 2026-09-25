// Resolved instead of an adapter under the `workerd` and `edge-light` export conditions. Adapters rely on
// node:async_hooks call ownership (their fail-open correlation) and node:crypto, which edge runtimes provide only
// behind deployment flags, so the target is unsupported and says so at import rather than failing later.
throw new Error('@caveman-ai/middleware requires Node.js >=22.12; edge runtimes (workerd, edge-light) are not supported. Run the adapter in a Node.js runtime.');
export {};
