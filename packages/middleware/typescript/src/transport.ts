import { AsyncLocalStorage } from 'node:async_hooks';
import { MiddlewareRuntime, sha256, type RecoveryBinding, type Scope } from '@caveman-ai/sdk/middleware';
import { MIDDLEWARE_VERSION, currentOwner, manifest, observe, passiveAttempt, plain, resolveScope, withOwner, type Attempt, type BudgetOptions, type ScopeSource } from './common.js';
import { guard } from './guard.js';
import { observeResponse } from './provider-response.js';
import { selectLeaves, type Protocol } from './provider-leaves.js';
import { parseWire, patchWire } from './wire.js';

/** `owner` is the Caveman fetch of the one client whose calls this context covers: a nested call made on another
 * client (a tool calling a second client on the same runtime) resolves its own scope instead of inheriting it. */
export interface RecoveryContext { runtime:MiddlewareRuntime;scope:Scope;binding:RecoveryBinding;overhead:string;logicalCallId:string;isRegistered?:()=>boolean;owner?:unknown }
/** An invocation whose recovery tool could not be registered: its calls pass through reporting `reason`. */
export interface UnboundContext { runtime:MiddlewareRuntime;reason:string;logicalCallId:string;owner?:unknown }
const recoveryContexts=new AsyncLocalStorage<RecoveryContext|UnboundContext>();
export function withNativeRecovery<T>(context:RecoveryContext|UnboundContext,run:()=>T):T{return recoveryContexts.run(context,run);}
function registered(context:RecoveryContext):boolean{
  try{return context.isRegistered===undefined||context.isRegistered()===true;}catch{return false;}
}

function acceptsRecovery(root:unknown,protocol:Protocol,binding:RecoveryBinding):boolean{
  if(!plain(root)||!Array.isArray(root.tools))return false;
  // A forced tool or final structured-output call cannot promise that the
  // recovery executor is available. Keep those original contracts intact.
  if(root.response_format!=null||root.output_format!=null||
    (plain(root.output_config)&&root.output_config.format!=null)||
    (protocol==='openai-responses'&&plain(root.text)&&root.text.format!=null))return false;
  const choice=root.tool_choice;
  if(choice!==undefined&&choice!=='auto'&&!(plain(choice)&&choice.type==='auto'))return false;
  const offered=root.tools.map(t=>plain(t)&&protocol==='openai-chat'?t.function:t);
  if(offered.some(t=>!plain(t)||typeof t.name!=='string')||new Set(offered.map(t=>(t as Record<string,unknown>).name)).size!==offered.length)return false;
  if(protocol==='anthropic-messages'&&Array.isArray(root.messages)&&root.messages.some(message=>plain(message)&&Array.isArray(message.content)&&message.content.some(part=>plain(part)&&['tool_removal','tool_addition'].includes(String(part.type)))))return false;
  const matches=root.tools.filter(t=>plain(t)&&(protocol==='openai-chat'?plain(t.function)&&t.function.name===binding.name:t.name===binding.name));
  if(matches.length!==1)return false;
  const tool=protocol==='openai-chat'?matches[0].function:matches[0];
  const schema=protocol==='anthropic-messages'?tool.input_schema:tool.parameters;
  return tool.description===binding.description&&JSON.stringify(schema)===JSON.stringify(binding.inputSchema);
}

export interface FetchOptions extends BudgetOptions {
  runtime:MiddlewareRuntime;
  /** A scope, or a function called per request so one shared client can serve many users. */
  scope:ScopeSource;
  /** Preserve the exact fetch implementation already selected by the host. Default: the global fetch at call time. */
  fetch?:typeof globalThis.fetch;
  providerBaseURL:string;
  provider:'openai'|'anthropic'|'google';
  frameworkVersion:string;
  /** Explicit middleware ownership on a discovered Caveman inference route. */
  cavemanProxy?:boolean;
  /** Untested SDKs retain only a native transport delegate and local reports. */
  passiveReason?:string;
  /** Request bodies larger than this (UTF-16 units, default 16 MiB) pass through with `payload_budget`. */
  wireBytes?:number;
  /** Compress OpenAI Responses turns that OpenAI stores (`store` not false). Off by default: the compressed turn would
   * persist provider-side (`provider_state_retained`). */
  allowStoredResponses?:boolean;
  /** Called on a compress-mode call that no recovery context covers, the path that cannot compress. */
  onUnbound?:()=>void;
}
const WIRE_BYTES=16<<20;

/** The fetch a client was built with (a private SDK field, so read defensively), else the global one: omitting the
 * `fetch` option keeps the client's own transport. */
export function clientFetch(client:unknown):typeof globalThis.fetch{
  const own=(client as {fetch?:unknown}).fetch;
  return typeof own==='function'?own as typeof globalThis.fetch:(input,init)=>globalThis.fetch(input,init);
}

function protocolFor(url:URL,provider:FetchOptions['provider']):Protocol|null{
  if(provider==='openai'&&url.pathname.endsWith('/chat/completions'))return 'openai-chat';
  if(provider==='openai'&&url.pathname.endsWith('/responses'))return 'openai-responses';
  if(provider==='anthropic'&&url.pathname.endsWith('/messages'))return 'anthropic-messages';
  if(provider==='google'&&/:streamGenerateContent$|:generateContent$/.test(url.pathname))return 'google-genai';
  return null;
}

/** Fetch injection retains SDK-native promises, parsers, streams and retries. */
export function createCavemanFetch(options:FetchOptions):typeof globalThis.fetch{
  const base=new URL(options.providerBaseURL);
  const send:typeof globalThis.fetch=options.fetch??((input,init)=>globalThis.fetch(input,init));
  const cavemanFetch:typeof globalThis.fetch=async (input,init)=>{
    const url=new URL(input instanceof Request?input.url:String(input));
    const protocol=protocolFor(url,options.provider);
    const parent=currentOwner();
    // Embeddings, files, models: no LLM request, so nothing to decide or report.
    if(!protocol)return send(input,init);
    const passiveReason=options.runtime.mode==='off'?'disabled':options.passiveReason??(url.origin!==base.origin||!url.pathname.startsWith(base.pathname.replace(/\/$/,''))?'unsupported_request':null);
    if(parent?.passive||parent?.runtime.mode==='off')return send(input,init);
    if(passiveReason){
      if(parent)return send(input,init);
      const passive=passiveAttempt(options.runtime,`${options.provider}-sdk`,passiveReason);
      observe(passive,'dispatch_intent');
      return withOwner(passive,()=>send(input,init));
    }
    const signal=init?.signal??(input instanceof Request?input.signal:undefined);
    signal?.throwIfAborted();
    let next=init;
    let attempt=parent;
    const body=typeof init?.body==='string'?init.body:null;
    const headers=new Headers(init?.headers??(input instanceof Request?input.headers:undefined));
    const signed=['digest','content-digest','content-md5','signature','signature-input','x-amz-content-sha256','dpop'].some(name=>headers.has(name))||/^(AWS4-HMAC|Signature )/.test(headers.get('authorization')??'');
    if(!parent&&options.runtime.mode!=='off'){
      const adapter=`${options.provider}-sdk`,store=recoveryContexts.getStore(),context=store?.owner===cavemanFetch?store:undefined;
      if(!context&&options.runtime.mode==='compress')options.onUnbound?.();
      const logicalCallId=context?.logicalCallId??crypto.randomUUID();
      const recovery=context&&'binding'in context?context:undefined;
      const scope=context&&'reason'in context?null:recovery?.scope??resolveScope(options.scope,undefined);
      attempt=scope?{runtime:options.runtime,scope,logicalCallId,attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,reason:'unsupported_shape',adapter}
        :passiveAttempt(options.runtime,adapter,context&&'reason'in context?context.reason:'recovery_unbound',logicalCallId);
      if(scope&&body&&!signed&&!headers.has('content-encoding')&&headers.get('content-type')?.includes('application/json')){
        const active=attempt;
        next=await guard(options.runtime,adapter,signal,()=>project(options,protocol,body,active,headers,init,recovery,signal),
          ()=>{Object.assign(active,{passive:true,reason:'adapter_error',optimization:null});return init;});
      }
    }
    if(attempt){
      const outbound=typeof next?.body==='string'?next.body:body;
      if(outbound!==null)attempt.wireSHA256=await sha256(outbound);
      if(options.cavemanProxy&&options.runtime.isRuntimeOrigin(url.href)){
        const controlled=new Headers(next?.headers??headers);controlled.set('x-cave-transforms','caveman.pass-through.v1');next={...next,headers:controlled};
      }
      signal?.throwIfAborted();
      if(!parent)observe(attempt,'dispatch_intent');
      try{
        const response=await withOwner(attempt,()=>send(input,next));
        if(parent)return response;
        if(!response.ok){observe(attempt,'failed');return response;}
        return observeResponse(response,attempt,signal);
      }catch(error){if(!parent)observe(attempt,signal?.aborted?'cancelled':'failed');throw error;}
    }
    return send(input,next);
  };
  return cavemanFetch;
}

async function project(options:FetchOptions,protocol:Protocol,body:string,attempt:Attempt,headers:Headers,init:RequestInit|undefined,
  recovery:RecoveryContext|undefined,signal:AbortSignal|null|undefined):Promise<RequestInit|undefined>{
  if(body.length>(options.wireBytes??WIRE_BYTES)){attempt.reason='payload_budget';return init;}
  const wire=parseWire(body,options.wireBytes??WIRE_BYTES);
  // Responses stores the turn it receives unless store:false; a stored compressed turn would outlive this call.
  const root=wire?.value;
  if(protocol==='openai-responses'&&plain(root)&&root.store!==false&&!options.allowStoredResponses){attempt.reason='provider_state_retained';return init;}
  const selection=wire?selectLeaves(wire,protocol):null;
  if(!wire||!selection)return init;
  const context=await manifest(selection.context,options.manifestBytes);
  const bound=recovery&&registered(recovery)&&options.runtime.ownsBinding(recovery.binding,attempt.scope)&&acceptsRecovery(wire.value,protocol,recovery.binding)?recovery:null;
  const outcome=await options.runtime.optimize({scope:attempt.scope,adapter:{id:`${options.provider}-sdk`,version:MIDDLEWARE_VERSION,framework_version:options.frameworkVersion,serialization_revision:`${protocol}-wire-v1`},
    model:plain(wire.value)&&typeof wire.value.model==='string'?{provider:options.provider,id:wire.value.model,protocol}:null,...context,candidates:selection.leaves.map((leaf,i)=>({id:`leaf-${i}`,sourceId:JSON.stringify(leaf.path).replace(/[^a-zA-Z0-9._:/-]/g,'_'),content:leaf.value})),
    binding:bound?.binding??null,...(bound?{recoveryOverheadText:bound.overhead}:{}),logicalCallId:attempt.logicalCallId,attemptId:attempt.attemptId,
    ...(signal?{signal}:{}),});
  // A prepared replacement is associated with dispatch only after its
  // unchanged native executor is re-attested and the wire patch applied.
  attempt.optimization=outcome.replacements.length===0?outcome:null;
  const lost=!!bound&&!registered(bound);
  if(outcome.replacements.length)attempt.reason=lost?'recovery_unavailable':'invalid_plan';
  const patches=outcome.replacements.map(r=>({leaf:selection.leaves[Number(r.segment_id.slice(5))]!,replacement:r.text}));
  if(!patches.length||!patches.every(p=>p.leaf)||lost)return init;
  const modified=patchWire(body,patches);
  if(modified===null)return init;
  attempt.optimization=outcome;
  if(headers.has('content-length'))headers.set('content-length',String(new TextEncoder().encode(modified).length));
  return {...init,body:modified,headers};
}
