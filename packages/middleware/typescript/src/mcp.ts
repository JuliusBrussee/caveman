import type { Client } from '@modelcontextprotocol/sdk/client/index.js';
import type { CallToolResult, Tool } from '@modelcontextprotocol/sdk/types.js';
import { MiddlewareRuntime, sha256, type ManifestItem, type Scope } from '@caveman-ai/sdk/middleware';
import { currentOwner, plain } from './common.js';
import { adapterCompatible, frameworkVersion } from './compatibility.js';

/** A native MCP definition and the callable actually registered in the host. */
export interface MCPToolBinding {
  tool: Tool;
  execute: (arguments_: Record<string, unknown>, options?: Parameters<Client['callTool']>[2]) => Promise<CallToolResult>;
}

/** Preserve the existing client's transport, IDs, auth and result validation. */
export function bindMCPTool(client: Client, tool: Tool): MCPToolBinding {
  return { tool, execute: (arguments_, options) => client.callTool({ name: tool.name, arguments: arguments_ }, undefined, options) as Promise<CallToolResult> };
}

export interface MCPHostOptions { runtime: MiddlewareRuntime; scope: Scope; serverId: string; protocolVersion: string }

/** Native host views only. No MCP framing, server or inference loop is added. */
export class CavemanMCPHost {
  readonly recovery: MCPToolBinding;
  private readonly binding;
  private readonly adapter;
  private readonly recoveryExecutor;
  private readonly recoveryDefinition;
  private readonly versionSupported;
  constructor(private readonly options: MCPHostOptions) {
    if (!options.serverId || !options.protocolVersion) throw new TypeError('Provide the native server identity and negotiated MCP protocol version');
    this.versionSupported = adapterCompatible('mcp');
    if (!this.versionSupported && options.runtime.mode !== 'off') options.runtime.decline('unsupported_version');
    this.binding = options.runtime.recovery(options.scope);
    this.adapter = { id: 'mcp', version: '0.1.0', framework_version: frameworkVersion('@modelcontextprotocol/sdk') ?? 'unknown', serialization_revision: `mcp-native-${options.protocolVersion}-v1` };
    this.recovery = {
      tool: { name: this.binding.name, description: this.binding.description, inputSchema: structuredClone(this.binding.inputSchema) as Tool['inputSchema'] },
      execute: async (arguments_, options) => {
        if (typeof arguments_.handle !== 'string') throw new TypeError('Recovery requires its scoped handle');
        const page = await this.binding.execute({ ...arguments_, handle: arguments_.handle }, options?.signal ? { signal: options.signal } : undefined);
        return { content: [{ type: 'text', text: JSON.stringify(page) }] };
      },
    };
    this.recoveryExecutor = this.recovery.execute;
    this.recoveryDefinition = JSON.stringify(this.recovery.tool);
  }

  register(tools: readonly MCPToolBinding[]): MCPToolBinding[] {
    return !this.versionSupported || this.options.runtime.mode !== 'compress' || tools.some(item => item.tool.name === this.recovery.tool.name) ? [...tools] : [...tools, this.recovery];
  }

  /** Persist the original result; pass only this copied view to the model.
   * The host supplies its original append-only context manifest across resumes.
   * Final provider serialization and dispatch are outside this result boundary. */
  async projectResult(result: CallToolResult, options: {
    tool: Tool; callId: string; contextManifest: readonly ManifestItem[];
    registeredTools?: readonly MCPToolBinding[]; sequence?: number; signal?: AbortSignal;
  }): Promise<CallToolResult> {
    const report = (reason: string) => this.options.runtime.report(null, { reason, adapter: this.adapter.id });
    if (options.signal?.aborted && !currentOwner()) report('cancelled');
    options.signal?.throwIfAborted();
    if (currentOwner()) return result;
    if (!this.versionSupported || this.options.runtime.mode === 'off') { report('unsupported_version'); return result; }
    if (!plain(result) || !plain(options.tool) || !Array.isArray(result.content) ||
      result.isError || ('resultType' in result && result.resultType !== 'complete') || 'structuredContent' in result || options.tool.outputSchema !== undefined || options.tool.name.startsWith('caveman_')) {
      report('protected_result'); return result;
    }
    const candidates = [], indices = new Map<string, number>();
    for (let i = 0; i < result.content.length; i++) {
      const part = result.content[i];
      if (!plain(part) || part.type !== 'text' || typeof part.text !== 'string' || part.annotations?.audience && !part.annotations.audience.includes('assistant')) continue;
      const key = `mcp-${await sha256(JSON.stringify([this.options.serverId, options.tool.name, options.callId, i]))}`;
      indices.set(key, i);
      candidates.push({ id: key, sourceId: key, content: part.text });
    }
    if (!candidates.length) { report('no_candidate'); return result; }
    const registered = () => {
      const matching = options.registeredTools?.filter(item => item.tool.name === this.binding.name) ?? [];
      return this.options.runtime.ownsBinding(this.binding, this.options.scope) && matching.length === 1 && matching[0] === this.recovery &&
        this.recovery.execute === this.recoveryExecutor && JSON.stringify(this.recovery.tool) === this.recoveryDefinition;
    };
    const bound = registered();
    const outcome = await this.options.runtime.optimize({ scope: this.options.scope, adapter: this.adapter,
      candidates, manifest: options.contextManifest,
      ...(options.sequence !== undefined ? { sequence: options.sequence } : {}), ...(options.signal ? { signal: options.signal } : {}),
      ...(bound ? { binding: this.binding } : {}), recoveryOverheadText: JSON.stringify(this.recovery.tool) });
    if (!outcome.replacements.length) { this.options.runtime.report(outcome); return result; }
    if (bound && !registered()) { report('recovery_unavailable'); return result; }
    if (!outcome.replacements.every(replacement => indices.has(replacement.segment_id))) { report('invalid_plan'); return result; }
    const content = [...result.content];
    for (const replacement of outcome.replacements) {
      const i = indices.get(replacement.segment_id)!;
      content[i] = { ...content[i], text: replacement.text } as typeof content[number];
    }
    const view = { ...result, content };
    this.options.runtime.report(outcome);
    return view;
  }
}
