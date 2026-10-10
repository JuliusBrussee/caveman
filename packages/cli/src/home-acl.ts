import { win32 } from "node:path";

// The names caveman writes at the top of its home; a name continued by "." or
// "-" is a temp, lock, rotated log or SQLite file beside one of them. Mirrors
// ownedEntries in proxy/internal/securehome/securehome.go: change both together.
export const OWNED_HOME_ENTRIES = [
  "bin", "browse-profile", "candidates", "cli", "exports", "hooks", "integrations", "mcp", "mem", "openclaw",
  "packs", "provider-logins", "recall-hooks", "receipts", "reports", "run", "runtime", "tmp", "usage",
  "browse-chrome.log", "browse-session.json", "cache-warm-gaps.json", "caveman.db", "caveman.yaml", "ccr.db",
  "chatgpt-host-id", "cloud.json", "credentials", "modules.lock.json", "provider-logins.json", "proxy.log",
  "route-state.json",
  ".browse-session", ".cache-warm", ".caveman-sqlite",
];

// Whether a Windows home's permissions are not caveman's to rewrite: a drive
// root, a network path (\\server\share, or a mapped drive, whose real path is
// one), a junction or symlink (the permissions would land on its target), or
// a folder holding anything caveman did not write (CAVEMAN_HOME pointed at
// D:\work). icacls /inheritance:r replaces the permissions of everything below
// for good, and on a share can lock the user out.
export function leaveHomeAclAlone(home: string, seen: { names: string[]; resolved: string; link: boolean }): boolean {
  const clean = win32.resolve(home);
  if (seen.link || win32.dirname(clean) === clean || clean.startsWith("\\\\") || seen.resolved.startsWith("\\\\")) return true;
  return seen.names.some((name) => !OWNED_HOME_ENTRIES.some((entry) => name === entry || name.startsWith(`${entry}.`) || name.startsWith(`${entry}-`)));
}

// A Windows system tool by full path. A bare name is looked up in the current
// directory first, so a whoami.exe planted in a project would run instead.
export function systemTool(name: string, env: NodeJS.ProcessEnv = process.env): string {
  return win32.join(env.SystemRoot || "C:\\Windows", "System32", `${name}.exe`);
}
