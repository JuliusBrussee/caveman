import type OpenAI from 'openai';
import { VERSION } from 'openai/version';
import { MiddlewareRuntime, type Scope } from '@caveman-ai/sdk/middleware';
import { plain } from './common.js';
import { frameworkCompatible } from './compatibility.js';
import { createCavemanFetch, withNativeRecovery, type RecoveryContext } from './transport.js';

export interface OpenAIOptions {
  runtime:MiddlewareRuntime;
  scope:Scope;
  /** Pass the same fetch function used to construct the original client. */
  fetch:typeof globalThis.fetch;
  cavemanProxy?:boolean;
}

type Functions = Readonly<Record<string, (input: unknown) => unknown | Promise<unknown>>>;
export interface OpenAIToolLoop<T extends OpenAI, Tool> {
  readonly client: T;
  /** A fresh native definition list; changing it cannot change registration. */
  readonly tools: Tool[];
  /** Use this immutable table for every application-owned function dispatch. */
  readonly functions: Functions;
}
type ChatFunction = OpenAI.Chat.Completions.ChatCompletionFunctionTool;
type ResponseFunction = OpenAI.Responses.FunctionTool;
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-chat';tools:ChatFunction[];functions:Functions}):OpenAIToolLoop<T,ChatFunction>;
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-responses';tools:ResponseFunction[];functions:Functions}):OpenAIToolLoop<T,ResponseFunction>;
/** Native application-owned Chat/Responses calls, with no added scheduler. */
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-chat'|'openai-responses';tools:(ChatFunction|ResponseFunction)[];functions:Functions}):OpenAIToolLoop<T,ChatFunction|ResponseFunction>{
  const definitions=structuredClone(options.tools);
  const names=definitions.map(tool=>'function' in tool?tool.function.name:tool.name);
  if(!plain(options.functions)||names.some(name=>typeof name!=='string'||!name)||new Set(names).size!==names.length||names.includes('caveman_retrieve')||'caveman_retrieve' in options.functions)throw new TypeError('Duplicate or reserved caveman_retrieve tool name');
  if(names.length!==Object.keys(options.functions).length||names.some(name=>typeof options.functions[name]!=='function'))throw new TypeError('Every native function definition needs exactly one executor');
  let functions=Object.freeze({...options.functions});
  let context:RecoveryContext|undefined;
  if(options.runtime.mode==='compress'&&frameworkCompatible('openai',VERSION)){
    const binding=options.runtime.recovery(options.scope);
    const schema={name:binding.name,description:binding.description,parameters:binding.inputSchema};
    const definition=options.protocol==='openai-chat'?{type:'function' as const,function:schema}:{type:'function' as const,...schema,strict:false};
    definitions.push(definition);
    const execute=(input:unknown)=>binding.execute(input as never);
    functions=Object.freeze({...functions,[binding.name]:execute});
    context={runtime:options.runtime,scope:options.scope,binding,overhead:JSON.stringify(definition),logicalCallId:crypto.randomUUID(),
      isRegistered:()=>options.runtime.ownsBinding(binding,options.scope)&&functions[binding.name]===execute};
  }
  const serialized=JSON.stringify(definitions);
  return Object.freeze({client:wrapOpenAI(client,options,context,options.protocol),functions,get tools(){return JSON.parse(serialized);}});
}

/** A native withOptions clone; APIPromise, parsers, streams and runners survive. */
export function withCavemanOpenAI<T extends OpenAI>(client:T,options:OpenAIOptions):T{
  return wrapOpenAI(client,options);
}

function wrapOpenAI<T extends OpenAI>(client:T,options:OpenAIOptions,context?:RecoveryContext,protocol?:'openai-chat'|'openai-responses'):T{
  const versionSupported=frameworkCompatible('openai',VERSION);
  if(!versionSupported&&options.runtime.mode!=='off')options.runtime.decline('unsupported_version');
  const fetch=createCavemanFetch({...options,provider:'openai',providerBaseURL:client.baseURL,frameworkVersion:VERSION,
    ...(!versionSupported?{passiveReason:'unsupported_version' as const}:{})});
  const native=client.withOptions({fetch});
  if(!versionSupported||options.runtime.mode==='off')return native;
  const run=native.chat.completions.runTools.bind(native.chat.completions);
  native.chat.completions.runTools=((body:unknown,requestOptions?:unknown)=>{
    if(!plain(body)||!Array.isArray(body.tools)||options.runtime.mode!=='compress')return run(body as never,requestOptions as never);
    const names=body.tools.map(t=>plain(t)&&plain(t.function)?t.function.name||(typeof t.function.function==='function'?t.function.function.name:null):null);
    if(names.some(name=>typeof name!=='string'||!name)||new Set(names).size!==names.length||names.includes('caveman_retrieve'))return run(body as never,requestOptions as never);
    const binding=options.runtime.recovery(options.scope);
    const execute=(input:Parameters<typeof binding.execute>[0],runner:{controller:AbortController})=>binding.execute(input,{signal:runner.controller.signal});
    const recovery=Object.freeze({type:'function' as const,function:Object.freeze({name:binding.name,description:binding.description,parameters:binding.inputSchema,parse:JSON.parse,function:execute})});
    const params={...body,tools:[...body.tools,recovery]};
    const overhead=JSON.stringify({type:'function',function:{name:binding.name,description:binding.description,parameters:binding.inputSchema}});
    // The native runner snapshots its dispatch table before any callbacks. The
    // invocation-owned recovery entry cannot change after that snapshot.
    const isRegistered=()=>options.runtime.ownsBinding(binding,options.scope)&&recovery.function.function===execute&&recovery.function.parse===JSON.parse;
    return withNativeRecovery({runtime:options.runtime,scope:options.scope,binding,overhead,logicalCallId:crypto.randomUUID(),isRegistered},()=>run(params as never,requestOptions as never));
  }) as typeof native.chat.completions.runTools;
  if(context){
    const post=native.post.bind(native);
    native.post=(path,params)=>path===(protocol==='openai-chat'?'/chat/completions':'/responses')?withNativeRecovery(context,()=>post(path,params)):post(path,params);
  }
  const clone=native.withOptions.bind(native);
  native.withOptions=(next)=>{
    const nextFetch=(next.fetch??options.fetch) as typeof globalThis.fetch;
    return wrapOpenAI(clone({...next,fetch:nextFetch}),{...options,fetch:nextFetch},context,protocol);
  };
  return native;
}

export { createCavemanFetch } from './transport.js';
