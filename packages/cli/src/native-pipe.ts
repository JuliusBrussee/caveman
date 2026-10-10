import { createHash } from "node:crypto";
import { win32 } from "node:path";

// The Windows named pipe the native runtime listens on for this home. Mirrors
// SocketPath in proxy/internal/nativeruntime/server_windows.go: sha256 of the
// absolute home lowercased the way Go's strings.ToLower does, one code point to
// one. String.toLowerCase() is not that: it turns İ into i + U+0307 and a
// word-final Σ into ς, so a Turkish or all-caps Greek profile hashed to a pipe
// the runtime never opened. Keep the two in step; both test the same vector.
// ponytail: follows each runtime's Unicode tables, so a capital letter added
// after Go's (Unicode 15) can still differ; ASCII-only folding on both sides
// fixes that, once every installed proxy is new enough to agree.
export function nativePipePath(home: string): string {
  let folded = "";
  for (const char of win32.resolve(home)) folded += String.fromCodePoint(char.toLowerCase().codePointAt(0)!);
  return `\\\\.\\pipe\\caveman-native-${createHash("sha256").update(folded).digest("hex").slice(0, 16)}`;
}
