import { warnOnce, type MiddlewareRuntime } from '@caveman-ai/sdk/middleware';
import { inRange, installedFrameworkVersion, warnFrameworkMismatch } from './versions.js';

// No npm peerDependencies (Decision 2): optional peers made `npm install` fail with ERESOLVE. These ranges are the
// execution gate. `floor` is the pinned merge-gate version; `tested` lists every release the suite has passed.
const frameworks = {
  ai: { floor: '7.0.94', high: '8', tested: ['7.0.94', '7.0.114'], entry: 'ai' },
  openai: { floor: '7.12.1', high: '8', tested: ['7.12.1', '7.23.0'], entry: 'openai' },
  '@anthropic-ai/sdk': { floor: '0.124.0', high: '0.129', tested: ['0.124.0', '0.125.0', '0.126.0', '0.127.0', '0.128.0'], entry: '@anthropic-ai/sdk' },
  '@google/genai': { floor: '2.21.0', high: '3', tested: ['2.21.0', '2.24.0'], entry: '@google/genai' },
  langchain: { floor: '1.5.10', high: '2', tested: ['1.5.10', '1.5.12'], entry: 'langchain' },
  '@langchain/core': { floor: '1.2.9', high: '2', tested: ['1.2.9', '1.2.12'], entry: '@langchain/core/messages' },
  '@strands-agents/sdk': { floor: '1.17.0', high: '2', tested: ['1.17.0', '1.19.0'], entry: '@strands-agents/sdk' },
  '@mastra/core': { floor: '1.65.0', high: '2', tested: ['1.65.0', '1.70.0'], entry: '@mastra/core/agent' },
  '@modelcontextprotocol/sdk': { floor: '1.30.0', high: '2', tested: ['1.30.0', '1.30.1'], entry: '@modelcontextprotocol/sdk/client/index.js' },
} as const;

type Framework = keyof typeof frameworks;
// Decision 11: certified adapters are gated by the conformance suite; the rest are experimental. Each entry is gated
// only on the packages it imports at run time (C11); `@ai-sdk/provider` is imported for types only. `id` is the adapter
// id in reports and warnings.
const adapters = {
  'ai-sdk': { id: 'ai-sdk', tier: 'certified', frameworks: ['ai'] },
  openai: { id: 'openai-sdk', tier: 'certified', frameworks: ['openai'] },
  anthropic: { id: 'anthropic-sdk', tier: 'certified', frameworks: ['@anthropic-ai/sdk'] },
  langchain: { id: 'langchain', tier: 'certified', frameworks: ['langchain', '@langchain/core'] },
  /** withCavemanModel, CavemanChatModel and CavemanDocumentCompressor. */
  'langchain-core': { id: 'langchain', tier: 'certified', frameworks: ['@langchain/core'] },
  google: { id: 'google-sdk', tier: 'experimental', frameworks: ['@google/genai'] },
  strands: { id: 'strands', tier: 'experimental', frameworks: ['@strands-agents/sdk'] },
  mastra: { id: 'mastra', tier: 'experimental', frameworks: ['@mastra/core'] },
  mcp: { id: 'mcp', tier: 'experimental', frameworks: ['@modelcontextprotocol/sdk'] },
} as const satisfies Record<string, { id: string; tier: 'certified' | 'experimental'; frameworks: readonly Framework[] }>;

export type AdapterName = keyof typeof adapters;
export interface FrameworkCompatibility {
  package: string;
  installed_version: string | null;
  supported_range: string;
  /** The range floor: the pinned merge-gate version. */
  tested_version: string;
  /** Every release this package's suite has passed. */
  tested_versions: readonly string[];
  /** One of `tested_versions`; not proof of every framework feature or production quality. */
  tested: boolean;
  compatible: boolean;
  reason: 'compatible' | 'unsupported_version' | 'version_unavailable';
  action: string;
}

export function frameworkVersion(name: Framework): string | null {
  return installedFrameworkVersion(name, frameworks[name].entry);
}

export function frameworkCompatible(name: Framework, version = frameworkVersion(name)): boolean {
  const spec = frameworks[name];
  return inRange(version, spec.floor, spec.high);
}

export interface GateOptions {
  runtime: MiddlewareRuntime;
  /** Run against a known framework version outside the supported range (Decision 3). Test it first. */
  acceptFrameworkVersion?: boolean;
}
export type GateReason = 'unsupported_version' | 'version_unavailable';

/** Decision 3, at wrap time and never throwing. A known version outside the range declines unless accepted. An
 * unreadable version (bundled deploys) runs with a one-time `version_unverified` when `detect` finds the hooks the
 * adapter needs, else declines with `version_unavailable`. Strict mode surfaces a decline from ready()/preflight().
 * Returns the pass-through reason, or null to run. */
export function frameworkGate(adapter: AdapterName, options: GateOptions, detect: () => boolean = () => true,
  versions: Partial<Record<Framework, string | null>> = {}): GateReason | null {
  if (options.runtime.mode === 'off') return null;
  const id = adapters[adapter].id;
  let outside = false, unverified = false;
  for (const name of adapters[adapter].frameworks) {
    const version = name in versions ? versions[name] ?? null : frameworkVersion(name);
    if (!(name in versions)) warnFrameworkMismatch(name, version);
    if (version === null) unverified = true;
    else if (!frameworkCompatible(name, version) && !options.acceptFrameworkVersion) outside = true;
  }
  let reason: GateReason | null = outside ? 'unsupported_version' : null;
  if (!reason && unverified) {
    let found = false;
    try { found = detect(); } catch { /* a missing hook */ }
    if (found) warnOnce(id, 'version_unverified'); else reason = 'version_unavailable';
  }
  if (reason) {
    warnOnce(id, reason);
    options.runtime.decline(reason, id);
  }
  return reason;
}

/** Read-only, content-free local check. Does not import optional frameworks or contact the compression runtime. An
 * unreadable version is reported here as `version_unavailable`; the adapter itself feature-detects at wrap time. */
export function inspectFrameworkCompatibility(adapter: AdapterName): {
  schema_version: 1; adapter: AdapterName; tier: 'certified' | 'experimental'; compatible: boolean; frameworks: FrameworkCompatibility[];
} {
  const entry = Object.hasOwn(adapters, adapter) ? adapters[adapter] : undefined;
  if (!entry) throw new TypeError(`Unknown Caveman adapter: ${adapter}`);
  const checks: FrameworkCompatibility[] = entry.frameworks.map(name => {
    const spec = frameworks[name], version = frameworkVersion(name);
    const compatible = frameworkCompatible(name, version), tested = (spec.tested as readonly string[]).includes(version ?? '');
    const reason = compatible ? 'compatible' : version === null ? 'version_unavailable' : 'unsupported_version';
    const action = compatible
      ? tested ? 'Tested release detected; run your workload acceptance checks.' : `Range accepted; tested releases are ${spec.tested.join(', ')}. Run your workload acceptance checks.`
      : version === null
        ? `No installed ${name} metadata found. Bundled builds run after feature detection with a version_unverified warning; keep ${name} external to report its version.`
        : `Use ${name} ${spec.tested.at(-1)}, or set acceptFrameworkVersion after testing ${version}; until then calls pass through unchanged.`;
    return { package: name, installed_version: version, supported_range: `>=${spec.floor} <${spec.high}`,
      tested_version: spec.floor, tested_versions: spec.tested, tested, compatible, reason, action };
  });
  return { schema_version: 1, adapter, tier: entry.tier, compatible: checks.every(check => check.compatible), frameworks: checks };
}
