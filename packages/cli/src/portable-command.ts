import { readFileSync, statSync } from "node:fs";
import { dirname, resolve } from "node:path";

export type PortableInvocation = { command: string; args: string[] };

export function parseWindowsNodeShim(source: string): string | null {
  for (const line of source.split(/\r?\n/)) {
    if (!/(?:\bnode(?:\.exe)?\b|_prog)/i.test(line) || !/%\*/.test(line)) continue;
    const match = line.match(/"%(?:dp0%|~dp0)\\([^"\r\n]+\.(?:cjs|mjs|js))"\s+%\*/i);
    if (match) return match[1]!;
  }
  return null;
}

export function portableInvocation(
  command: string,
  args: readonly string[],
  platform: NodeJS.Platform = process.platform,
): PortableInvocation {
  if (platform !== "win32" || !/\.(?:cmd|bat)$/i.test(command)) {
    return { command, args: [...args] };
  }
  const stat = statSync(command);
  if (!stat.isFile() || stat.size > 256 * 1024) {
    throw new Error(`cannot safely launch Windows command shim: ${command}`);
  }
  const relativeScript = parseWindowsNodeShim(readFileSync(command, "utf8"));
  if (!relativeScript) {
    throw new Error(`cannot safely launch non-Node Windows command shim: ${command}; install a native .exe`);
  }
  const script = resolve(dirname(command), ...relativeScript.split(/[\\/]+/));
  if (!statSync(script).isFile()) {
    throw new Error(`Windows command shim target is missing: ${script}`);
  }
  return { command: process.execPath, args: [script, ...args] };
}
