import type OpenAI from 'openai';
import { VERSION } from 'openai/version';
import { MiddlewareRuntime, recoveryInputSchema, recoveryToolDescription, warnOnce, type RetrieveArgs, type Scope } from '@caveman-ai/sdk/middleware';
import { bindRecovery, hintRecovery, nameConflict, plain, resolveScope, type BudgetOptions, type ScopeSource } from './common.js';
import { frameworkGate, type GateOptions } from './compatibility.js';
import { guardSync } from './guard.js';
import { createCavemanFetch, withNativeRecovery, type FetchOptions, type RecoveryContext } from './transport.js';

export interface OpenAIOptions extends GateOptions, BudgetOptions, Pick<FetchOptions, 'wireBytes' | 'allowStoredResponses'> {
  runtime:MiddlewareRuntime;
  /** A scope, or a function called per request so one shared client can serve many users. */
  scope:ScopeSource;
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
const ID='openai-sdk';
const gate=(options:OpenAIOptions)=>frameworkGate('openai',options,undefined,{openai:VERSION});
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-chat';tools:ChatFunction[];functions:Functions}):OpenAIToolLoop<T,ChatFunction>;
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-responses';tools:ResponseFunction[];functions:Functions}):OpenAIToolLoop<T,ResponseFunction>;
/** Native application-owned Chat/Responses calls, with no added scheduler. The OpenAI entry point that compresses. */
export function withCavemanOpenAITools<T extends OpenAI>(client:T,options:OpenAIOptions & {protocol:'openai-chat'|'openai-responses';tools:(ChatFunction|ResponseFunction)[];functions:Functions}):OpenAIToolLoop<T,ChatFunction|ResponseFunction>{
  const definitions=structuredClone(options.tools);
  let functions=Object.freeze({...options.functions});
  const blocked=gate(options);
  let context:(()=>RecoveryContext|null)|undefined,conflict=false;
  if(options.runtime.mode==='compress'&&!blocked){
    const names=definitions.map(tool=>'function' in tool?tool.function.name:tool.name);
    // Nothing raises at wrap time (spec §8): a clash with caveman_retrieve, or a malformed table, disables recovery.
    if(names.includes('caveman_retrieve')||'caveman_retrieve' in functions)conflict=!!nameConflict(options.runtime,ID);
    else if(!plain(options.functions)||names.some(name=>typeof name!=='string'||!name)||new Set(names).size!==names.length||
      names.length!==Object.keys(functions).length||names.some(name=>typeof functions[name]!=='function'))warnOnce(ID,'invalid_configuration');
    else{
      const schema={name:'caveman_retrieve',description:recoveryToolDescription,parameters:structuredClone(recoveryInputSchema)};
      const definition=options.protocol==='openai-chat'?{type:'function' as const,function:schema}:{type:'function' as const,...schema,strict:false};
      definitions.push(definition);
      const execute=(input:unknown)=>options.runtime.retrieve(resolveScope(options.scope,undefined) as Scope,input as RetrieveArgs);
      functions=Object.freeze({...functions,caveman_retrieve:execute});
      const overhead=JSON.stringify(definition);
      // Per request, so a scope function resolves in each caller's own context.
      context=()=>{
        const binding=bindRecovery(options.runtime,resolveScope(options.scope,undefined));
        return binding&&{runtime:options.runtime,scope:binding.scope,binding,overhead,logicalCallId:crypto.randomUUID(),isRegistered:()=>functions.caveman_retrieve===execute};
      };
    }
  }
  const serialized=JSON.stringify(definitions);
  return Object.freeze({client:wrapOpenAI(client,options,blocked,{context,conflict,protocol:options.protocol}),functions,get tools(){return JSON.parse(serialized);}});
}

/** A native withOptions clone; APIPromise, parsers, streams and runners survive. `chat.completions.runTools`
 * compresses; plain `create` calls cannot bind the recovery tool and report `recovery_unbound`. */
export function withCavemanOpenAI<T extends OpenAI>(client:T,options:OpenAIOptions):T{
  const blocked=gate(options);
  if(!blocked)hintRecovery(options.runtime,ID,'withCavemanOpenAI create()','chat.completions.runTools or withCavemanOpenAITools');
  return wrapOpenAI(client,options,blocked);
}

interface Tools { context:(()=>RecoveryContext|null)|undefined; conflict:boolean; protocol:'openai-chat'|'openai-responses' }
function wrapOpenAI<T extends OpenAI>(client:T,options:OpenAIOptions,blocked:string|null,tools?:Tools):T{
  const fetch=createCavemanFetch({...options,provider:'openai',providerBaseURL:client.baseURL,frameworkVersion:VERSION,...(blocked?{passiveReason:blocked}:{})});
  const native=client.withOptions({fetch});
  if(blocked||options.runtime.mode==='off')return native;
  guardSync(options.runtime,ID,()=>{
    const run=native.chat.completions.runTools.bind(native.chat.completions);
    native.chat.completions.runTools=((body:unknown,requestOptions?:unknown)=>{
      if(!plain(body)||!Array.isArray(body.tools)||options.runtime.mode!=='compress')return run(body as never,requestOptions as never);
      const bodyTools:unknown[]=body.tools;
      return guardSync(options.runtime,ID,()=>{
        const names=bodyTools.map(t=>plain(t)&&plain(t.function)?t.function.name||(typeof t.function.function==='function'?t.function.function.name:null):null);
        const logicalCallId=crypto.randomUUID();
        if(names.includes('caveman_retrieve'))return withNativeRecovery({runtime:options.runtime,reason:nameConflict(options.runtime,ID),logicalCallId},()=>run(body as never,requestOptions as never));
        const binding=names.some(name=>typeof name!=='string'||!name)||new Set(names).size!==names.length?null:bindRecovery(options.runtime,resolveScope(options.scope,undefined));
        if(!binding)return run(body as never,requestOptions as never);
        const execute=(input:Parameters<typeof binding.execute>[0],runner:{controller:AbortController})=>binding.execute(input,{signal:runner.controller.signal});
        const recovery=Object.freeze({type:'function' as const,function:Object.freeze({name:binding.name,description:binding.description,parameters:binding.inputSchema,parse:JSON.parse,function:execute})});
        const params={...body,tools:[...bodyTools,recovery]};
        const overhead=JSON.stringify({type:'function',function:{name:binding.name,description:binding.description,parameters:binding.inputSchema}});
        // The native runner snapshots its dispatch table before any callbacks. The
        // invocation-owned recovery entry cannot change after that snapshot.
        const isRegistered=()=>options.runtime.ownsBinding(binding,binding.scope)&&recovery.function.function===execute&&recovery.function.parse===JSON.parse;
        return withNativeRecovery({runtime:options.runtime,scope:binding.scope,binding,overhead,logicalCallId,isRegistered},()=>run(params as never,requestOptions as never));
      },()=>run(body as never,requestOptions as never));
    }) as typeof native.chat.completions.runTools;
  },()=>undefined);
  if(tools?.context||tools?.conflict){
    const {context,conflict,protocol}=tools;
    // `post` is an SDK-internal seam: if it moves, calls run recovery-free instead of failing.
    guardSync(options.runtime,ID,()=>{
      const post=native.post.bind(native);
      native.post=(path,params)=>{
        if(path!==(protocol==='openai-chat'?'/chat/completions':'/responses'))return post(path,params);
        const current=conflict?{runtime:options.runtime,reason:'recovery_name_conflict',logicalCallId:crypto.randomUUID()}:context?.();
        return current?withNativeRecovery(current,()=>post(path,params)):post(path,params);
      };
    },()=>undefined);
  }
  const clone=native.withOptions.bind(native);
  native.withOptions=(next)=>{
    const nextFetch=(next.fetch??options.fetch) as typeof globalThis.fetch;
    return wrapOpenAI(clone({...next,fetch:nextFetch}),{...options,fetch:nextFetch},blocked,tools);
  };
  return native;
}

export { createCavemanFetch } from './transport.js';
