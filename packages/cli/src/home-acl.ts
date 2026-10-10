import { win32 } from "node:path";

// The names caveman writes at the top of its home; a name continued by
// OWNED_HOME_SUFFIX is a temp, lock, rotated log or SQLite file beside one of
// them, and a dot-named temp prefix continued by "-" is a temp file. Mirrors
// ownedEntries, homeMarkers and ownedSuffix in
// proxy/internal/securehome/securehome.go: change both together.
export const OWNED_HOME_ENTRIES = [
  "bin", "browse-profile", "candidates", "cli", "exports", "hooks", "integrations", "mcp", "mem", "openclaw",
  "packs", "provider-logins", "recall-hooks", "receipts", "reports", "run", "runtime", "tmp", "usage",
  "browse-chrome.log", "browse-session.json", "cache-warm-gaps.json", "caveman.db", "caveman.yaml", "ccr.db",
  "chatgpt-host-id", "cloud.json", "credentials", "modules.lock.json", "provider-logins.json", "proxy.log",
  "route-state.json",
  ".browse-session", ".cache-warm", ".caveman-sqlite", ".install.lock",
];

// Files no other program writes. bin, run, tmp or reports alone are any tools
// folder, so a home holding anything is caveman's only with one of these.
export const HOME_MARKERS = [
  "caveman.db", "caveman.yaml", "ccr.db", "cloud.json", "modules.lock.json", "provider-logins.json", "route-state.json",
];

// SQLite's -wal, -shm and -journal, a rotated log's .1, a temp (.tmp,
// .<pid>.<random>.tmp), a lock (.lock, .<purpose>.lock), a lock being broken,
// a broken lock set aside (.<pid>.<ms>.stale).
export const OWNED_HOME_SUFFIX = /^(?:-wal|-shm|-journal|\.\d+|(?:\.[^.]+)*\.tmp|(?:\.[^.]+)*\.stale|(?:\.[^.]+)?\.lock(?:\.break\..+)?)$/;

// Whether a Windows home's permissions are not caveman's to rewrite: a drive
// root, a network path (\\server\share, or a mapped drive, whose real path is
// one), a junction or symlink (the permissions would land on its target), or
// a folder holding anything caveman did not write, or none of its own files
// (CAVEMAN_HOME pointed at D:\work or D:\tools). icacls /inheritance:r replaces
// the permissions of everything below for good, and on a share can lock the
// user out.
export function leaveHomeAclAlone(home: string, seen: { names: string[]; resolved: string; link: boolean }): boolean {
  const clean = win32.resolve(home);
  if (seen.link || win32.dirname(clean) === clean || clean.startsWith("\\\\") || seen.resolved.startsWith("\\\\")) return true;
  const owned = (name: string) => OWNED_HOME_ENTRIES.some((entry) => {
    const rest = name.startsWith(entry) ? name.slice(entry.length) : undefined;
    return rest !== undefined && (rest === "" || OWNED_HOME_SUFFIX.test(rest) || (entry.startsWith(".") && rest.startsWith("-")));
  });
  return !seen.names.every(owned) || (seen.names.length > 0 && !seen.names.some((name) => HOME_MARKERS.includes(name)));
}

// A Windows system tool by full path. A bare name is looked up in the current
// directory first, so a whoami.exe planted in a project would run instead.
export function systemTool(name: string, env: NodeJS.ProcessEnv = process.env): string {
  return win32.join(env.SystemRoot || "C:\\Windows", "System32", `${name}.exe`);
}
