import { plain } from './common.js';
import { pathKey, type WireJSON, type StringLeaf } from './wire.js';

export type Protocol = 'openai-chat' | 'openai-responses' | 'anthropic-messages' | 'google-genai';
export interface Selection { context: unknown[]; leaves: StringLeaf[] }

export function selectLeaves(wire: WireJSON, protocol: Protocol): Selection | null {
  if (!plain(wire.value)) return null;
  const root = wire.value, leaves: StringLeaf[] = [];
  const calls = new Map<string, { name: string; position: number } | null>(), results = new Map<string, number>();
  const only = (value: unknown, keys: readonly string[]): value is Record<string, unknown> => plain(value) && Object.keys(value).every(key => keys.includes(key));
  const add = (path: (string|number)[]) => { const leaf = wire.strings.get(pathKey(path)); if (leaf) leaves.push(leaf); };
  const call = (id: unknown, name: unknown, position: number, valid: boolean) => {
    if (typeof id === 'string' && id) calls.set(id, !calls.has(id) && valid ? { name: name as string, position } : null);
  };
  const result = (id: unknown) => { if (typeof id === 'string') results.set(id, (results.get(id) ?? 0) + 1); };
  const matched = (id: unknown, position: number, name?: unknown) => {
    const source = typeof id === 'string' ? calls.get(id) : null;
    return !!source && source.position < position && source.name !== 'caveman_retrieve' && results.get(id as string) === 1 && (name === undefined || name === source.name);
  };
  const textParts = (content: unknown, path: (string|number)[], cacheControl = false) => {
    if (typeof content === 'string') add(path);
    else if (Array.isArray(content)) {
      const keys = cacheControl ? ['type', 'text', 'cache_control'] : ['type', 'text'];
      // Mixed/cited/unknown contracts protect the entire result.
      if (content.every(part => only(part, keys) && part.type === 'text' && typeof part.text === 'string')) {
        content.forEach((_, i) => add([...path, i, 'text']));
      }
    }
  };
  if (protocol === 'openai-chat' && Array.isArray(root.messages)) {
    root.messages.forEach((message, mi) => {
      if (!plain(message)) return;
      if (message.role === 'assistant' && Array.isArray(message.tool_calls)) {
        for (const item of message.tool_calls) {
          if (!plain(item)) continue;
          const fn = item.function;
          const valid = only(item, ['id', 'type', 'function', 'index']) && item.type === 'function' &&
            (item.index === undefined || (typeof item.index === 'number' && Number.isSafeInteger(item.index) && item.index >= 0)) &&
            only(fn, ['name', 'arguments']) && typeof fn.name === 'string' && !!fn.name && typeof fn.arguments === 'string';
          call(item.id, plain(fn) ? fn.name : undefined, mi, valid);
        }
      } else if (message.role === 'tool') result(message.tool_call_id);
    });
    root.messages.forEach((message, mi) => {
      if (!only(message, ['role', 'tool_call_id', 'content', 'name', 'is_error', 'cache_breakpoint']) || message.role !== 'tool' ||
          (message.is_error !== undefined && message.is_error !== false) || !matched(message.tool_call_id, mi, message.name)) return;
      textParts(message.content, ['messages', mi, 'content']);
    });
    return { context: root.messages, leaves };
  }
  if (protocol === 'openai-responses' && Array.isArray(root.input)) {
    if (root.previous_response_id || root.conversation) return null;
    root.input.forEach((item, i) => {
      if (!plain(item)) return;
      if (item.type === 'function_call') {
        const valid = only(item, ['type', 'call_id', 'name', 'arguments', 'id', 'status', 'parsed_arguments', 'caller', 'namespace']) &&
          (item.caller === undefined || item.caller === null) && (item.namespace === undefined || item.namespace === null) &&
          (item.parsed_arguments === undefined || item.parsed_arguments === null || plain(item.parsed_arguments)) && typeof item.name === 'string' && !!item.name &&
          typeof item.arguments === 'string' && (item.status === undefined || item.status === null || item.status === 'completed');
        call(item.call_id, item.name, i, valid);
      } else if (item.type === 'function_call_output') result(item.call_id);
    });
    root.input.forEach((item, i) => {
      if (only(item, ['type', 'call_id', 'output', 'id', 'status']) && item.type === 'function_call_output' &&
          (item.status === undefined || item.status === null || item.status === 'completed') && matched(item.call_id, i) && typeof item.output === 'string') add(['input', i, 'output']);
    });
    return { context: root.input, leaves };
  }
  if (protocol === 'anthropic-messages' && Array.isArray(root.messages)) {
    root.messages.forEach((message, mi) => {
      if (!plain(message) || !Array.isArray(message.content)) return;
      for (const part of message.content) {
        if (!plain(part)) continue;
        if (message.role === 'assistant' && part.type === 'tool_use') {
          const valid = only(part, ['type', 'id', 'name', 'input', 'cache_control']) && typeof part.name === 'string' && !!part.name && plain(part.input);
          call(part.id, part.name, mi, valid);
        } else if (part.type === 'tool_result') result(part.tool_use_id);
      }
    });
    root.messages.forEach((message, mi) => {
      if (!plain(message) || message.role !== 'user' || !Array.isArray(message.content)) return;
      message.content.forEach((part, pi) => {
        if (!only(part, ['type', 'tool_use_id', 'content', 'is_error', 'cache_control']) || part.type !== 'tool_result' ||
            (part.is_error !== undefined && part.is_error !== false) || !matched(part.tool_use_id, mi)) return;
        textParts(part.content, ['messages', mi, 'content', pi, 'content'], true);
      });
    });
    return { context: root.messages, leaves };
  }
  if (protocol === 'google-genai' && Array.isArray(root.contents)) {
    root.contents.forEach((message,mi) => {
      if (!plain(message) || !Array.isArray(message.parts)) return;
      message.parts.forEach((part,pi) => {
        if (!plain(part) || !plain(part.functionResponse) || part.functionResponse.name === 'caveman_retrieve' || !plain(part.functionResponse.response) || 'error' in part.functionResponse.response) return;
        // Google function response records stay records; only the explicitly
        // model-visible string output is eligible, never arbitrary nested data.
        add(['contents',mi,'parts',pi,'functionResponse','response','output']);
      });
    });
    return { context:root.contents,leaves };
  }
  return null;
}
