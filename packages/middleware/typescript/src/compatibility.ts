import { inRange, installedFrameworkVersion } from './versions.js';

// Package-manager peers are optional and unconstrained so unrelated adapters do
// not force an application's provider versions. These are the execution gates.
const frameworks = {
  ai: { tested: '7.0.94', high: '8', entry: 'ai' },
  '@ai-sdk/provider': { tested: '4.0.11', high: '5', entry: '@ai-sdk/provider' },
  openai: { tested: '7.12.1', high: '8', entry: 'openai' },
  '@anthropic-ai/sdk': { tested: '0.124.0', high: '0.125', entry: '@anthropic-ai/sdk' },
  '@google/genai': { tested: '2.21.0', high: '3', entry: '@google/genai' },
  langchain: { tested: '1.5.10', high: '2', entry: 'langchain' },
  '@langchain/core': { tested: '1.2.9', high: '2', entry: '@langchain/core/messages' },
  '@langchain/langgraph': { tested: '1.4.14', high: '2', entry: '@langchain/langgraph' },
  '@strands-agents/sdk': { tested: '1.17.0', high: '2', entry: '@strands-agents/sdk' },
  '@mastra/core': { tested: '1.65.0', high: '2', entry: '@mastra/core/agent' },
  '@modelcontextprotocol/sdk': { tested: '1.30.0', high: '2', entry: '@modelcontextprotocol/sdk/client/index.js' },
} as const;

type Framework = keyof typeof frameworks;
const adapters = {
  'ai-sdk': ['ai', '@ai-sdk/provider'],
  openai: ['openai'],
  anthropic: ['@anthropic-ai/sdk'],
  google: ['@google/genai'],
  langchain: ['langchain', '@langchain/core', '@langchain/langgraph'],
  strands: ['@strands-agents/sdk'],
  mastra: ['@mastra/core'],
  mcp: ['@modelcontextprotocol/sdk'],
} as const satisfies Record<string, readonly Framework[]>;

export type AdapterName = keyof typeof adapters;
export interface FrameworkCompatibility {
  package: string;
  installed_version: string | null;
  supported_range: string;
  tested_version: string;
  /** Exact test pin, not proof of every framework feature or production quality. */
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
  return inRange(version, spec.tested, spec.high);
}

export function adapterCompatible(adapter: AdapterName): boolean {
  return adapters[adapter].every(name => frameworkCompatible(name));
}

/** Read-only, content-free local check. Does not import optional frameworks or
 * contact the compression runtime. Unknown metadata never enables compression.
 * Node ESM bundles must keep framework packages external with metadata intact. */
export function inspectFrameworkCompatibility(adapter: AdapterName): {
  schema_version: 1; adapter: AdapterName; compatible: boolean; frameworks: FrameworkCompatibility[];
} {
  const entries = adapters[adapter];
  if (!entries) throw new TypeError(`Unknown Caveman adapter: ${adapter}`);
  const checks: FrameworkCompatibility[] = entries.map(name => {
    const spec = frameworks[name], version = frameworkVersion(name);
    const compatible = frameworkCompatible(name, version), tested = version === spec.tested;
    const reason = compatible ? 'compatible' : version === null ? 'version_unavailable' : 'unsupported_version';
    const action = compatible
      ? tested ? 'Exact test pin detected; run your workload acceptance checks.' : `Range accepted; only ${spec.tested} is the tested pin. Run your workload acceptance checks.`
      : version === null
        ? `Install ${name}@${spec.tested}. For Node ESM bundles, keep framework packages external and preserve package.json metadata.`
        : `Use ${name}@${spec.tested}, or keep optimization bypassed until this version is supported.`;
    return { package: name, installed_version: version, supported_range: `>=${spec.tested} <${spec.high}`,
      tested_version: spec.tested, tested, compatible, reason, action };
  });
  return { schema_version: 1, adapter, compatible: checks.every(check => check.compatible), frameworks: checks };
}
