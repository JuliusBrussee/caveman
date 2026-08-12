import { createRequire, syncBuiltinESMExports } from "node:module";

const require = createRequire(import.meta.url);

export function installNetworkDeny(): void {
  const denied = () => {
    const error = new Error("cave_sandbox_network_denied");
    (error as NodeJS.ErrnoException).code = "ERR_ACCESS_DENIED";
    throw error;
  };
  globalThis.fetch = denied as typeof globalThis.fetch;
  globalThis.WebSocket = class {
    constructor() {
      denied();
    }
  } as unknown as typeof globalThis.WebSocket;
  for (const name of ["node:http", "node:https", "node:net", "node:tls", "node:dgram", "node:http2", "node:dns"]) {
    const module = require(name) as Record<string, unknown>;
    for (const key of [
      "connect", "createConnection", "createServer", "get", "request", "createSocket",
      "lookup", "resolve", "resolve4", "resolve6", "resolveAny", "reverse",
    ]) {
      if (key in module) {
        try {
          module[key] = denied;
        } catch {
          // CommonJS mutation plus syncBuiltinESMExports covers mutable APIs.
        }
      }
    }
  }
  syncBuiltinESMExports();
}

export function networkIsolatedNode(nodeArgs: readonly string[]): {
  command: string;
  args: string[];
} {
  if (process.platform === "darwin") {
    return {
      command: "/usr/bin/sandbox-exec",
      args: [
        "-p",
        "(version 1)(allow default)(deny network*)",
        process.execPath,
        "--no-addons",
        ...nodeArgs,
      ],
    };
  }
  if (process.platform === "linux") {
    return {
      command: "/usr/bin/unshare",
      args: [
        "--user",
        "--map-root-user",
        "--net",
        "--",
        process.execPath,
        "--no-addons",
        ...nodeArgs,
      ],
    };
  }
  throw new Error("cave_sandbox_os_network_isolation_unavailable");
}
