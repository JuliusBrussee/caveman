// Running git against a repository whose contents we do not trust. A
// repository's own .git/config names programs git will run — core.fsmonitor
// runs while the index is refreshed, so `git status` alone is enough — which
// would let merely opening a session inside a freshly cloned untrusted
// repository execute code. Command-line -c beats repository config.
//
// Mirrors proxy/internal/gitsafe. Keep the two lists together.
export const HARDENED_GIT_ARGS = [
  "--no-pager",
  "-c", "core.fsmonitor=false",
  "-c", "core.hooksPath=/dev/null",
  "-c", "core.sshCommand=",
  "-c", "core.askPass=",
  "-c", "core.editor=true",
  "-c", "core.pager=cat",
  "-c", "core.alternateRefsCommand=",
  "-c", "diff.external=",
  "-c", "credential.helper=",
  "-c", "protocol.ext.allow=never",
  "-c", "uploadpack.packObjectsHook=",
];

// Argument list for a hardened git run rooted at `cwd`.
export function hardenedGitArgs(cwd: string, ...args: string[]): string[] {
  return ["-C", cwd, ...HARDENED_GIT_ARGS, ...args];
}

// Inherited GIT_* variables are dropped so an outer environment cannot
// redirect the index, config, or work tree either. System config
// (/etc/gitconfig) is deliberately still read — it is root-owned, so it is not
// part of this threat, and skipping it would discard org-wide `safe.directory`
// allowances and silently cost us repository state on shared machines.
export function hardenedGitEnv(base: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(base)) {
    if (!key.startsWith("GIT_")) env[key] = value;
  }
  env.GIT_TERMINAL_PROMPT = "0";
  env.GIT_OPTIONAL_LOCKS = "0";
  return env;
}
