// Windows-safe command invocation.
//
// `execFile("caveman", …)` on Windows resolves to whatever PATH offers first.
// npm/pnpm .bin directories park a NON-EXECUTABLE Unix shim under the bare name
// right next to the real `.CMD`, and `~/.caveman/bin/caveman` is likewise an
// extensionless script. Handing either to execFile fails with `spawn EFTYPE`,
// which is what took down every pi-extension hook call on Windows.
//
// The CLI's helper resolves through PATHEXT the way Windows does and unwraps
// Node, npm/npx and .exe-forwarding shims without a shell (#834). It is
// imported, not copied: esbuild inlines it into dist/, so the published package
// still has zero runtime dependencies, and a copy here had already fallen
// behind (no stock npm.cmd/npx.cmd, no .exe-forwarding shims).
//
// Fail-open by contract: the hook bridge treats a throw the same as a spawn
// error, and an unresolvable command falls through to the next candidate, so a
// shim we cannot parse must never take down the caller.
export * from "../../cli/src/portable-command.ts";
