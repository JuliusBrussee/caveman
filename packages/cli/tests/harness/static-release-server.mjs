#!/usr/bin/env node
import { createReadStream, statSync } from "node:fs";
import { createServer } from "node:http";
import { resolve, sep } from "node:path";

const root = resolve(process.argv[2] ?? "");
const port = Number(process.argv[3] ?? "8765");
if (!root || !Number.isInteger(port) || port <= 0) {
  throw new Error("usage: static-release-server.mjs <asset-root> [port]");
}

const server = createServer((request, response) => {
  if (request.method !== "GET" && request.method !== "HEAD") {
    response.writeHead(405).end();
    return;
  }
  const pathname = decodeURIComponent(new URL(request.url ?? "/", "http://release.invalid").pathname);
  const path = resolve(root, `.${pathname}`);
  if (path !== root && !path.startsWith(`${root}${sep}`)) {
    response.writeHead(403).end();
    return;
  }
  try {
    const stat = statSync(path);
    if (!stat.isFile()) throw new Error("not a file");
    response.writeHead(200, {
      "content-length": stat.size,
      "content-type": "application/octet-stream",
    });
    if (request.method === "HEAD") response.end();
    else createReadStream(path).pipe(response);
  } catch {
    response.writeHead(404).end();
  }
});

server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`static release server listening on 127.0.0.1:${port}\n`);
});
process.on("SIGTERM", () => server.close(() => process.exit(0)));
