import { AsyncLocalStorage } from 'node:async_hooks';
import { MiddlewareRuntime, sha256, type RecoveryBinding, type Scope } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, plain, withOwner, type Attempt } from './common.js';
import { observeResponse } from './provider-response.js';
import { selectLeaves, type Protocol } from './provider-leaves.js';
import { parseWire, patchWire } from './wire.js';

export interface RecoveryContext { runtime:MiddlewareRuntime;scope:Scope;binding:RecoveryBinding;overhead:string;logicalCallId:string;isRegistered?:()=>boolean }
const recoveryContexts=new AsyncLocalStorage<RecoveryContext>();
export function withNativeRecovery<T>(context:RecoveryContext,run:()=>T):T{return recoveryContexts.run(context,run);}
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

export interface FetchOptions {
  runtime:MiddlewareRuntime;
  scope:Scope;
  /** Preserve the exact fetch implementation already selected by the host. */
  fetch:typeof globalThis.fetch;
  providerBaseURL:string;
  provider:'openai'|'anthropic'|'google';
  frameworkVersion:string;
  /** Explicit middleware ownership on a discovered Caveman inference route. */
  cavemanProxy?:boolean;
  /** Untested SDKs retain only a native transport delegate and local reports. */
  passiveReason?:'unsupported_version';
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
  return async (input,init)=>{
    const url=new URL(input instanceof Request?input.url:String(input));
    const protocol=protocolFor(url,options.provider);
    const parent=currentOwner();
    const passiveReason=options.runtime.mode==='off'?'disabled':options.passiveReason??(!protocol||url.origin!==base.origin||!url.pathname.startsWith(base.pathname.replace(/\/$/,''))?'unsupported_endpoint':null);
    if(parent?.passive||parent?.runtime.mode==='off')return options.fetch(input,init);
    if(passiveReason){
      if(parent)return options.fetch(input,init);
      const passive:Attempt={runtime:options.runtime,scope:options.scope,logicalCallId:crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,
        passive:true,reason:passiveReason,adapter:`${options.provider}-sdk`};
      observe(passive,'dispatch_intent');
      return withOwner(passive,()=>options.fetch(input,init));
    }
    if(!protocol)return options.fetch(input,init);
    const signal=init?.signal??(input instanceof Request?input.signal:undefined);
    if(signal?.aborted&&!parent)options.runtime.report(null,{reason:'cancelled',adapter:`${options.provider}-sdk`});
    signal?.throwIfAborted();
    let next=init;
    let attempt=parent;
    const body=typeof init?.body==='string'?init.body:null;
    const headers=new Headers(init?.headers??(input instanceof Request?input.headers:undefined));
    const signed=['digest','content-digest','content-md5','signature','signature-input','x-amz-content-sha256','dpop'].some(name=>headers.has(name))||/^(AWS4-HMAC|Signature )/.test(headers.get('authorization')??'');
    if(!parent&&options.runtime.mode!=='off'){
      attempt={runtime:options.runtime,scope:options.scope,logicalCallId:recoveryContexts.getStore()?.logicalCallId??crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,
        reason:'unsupported_shape',adapter:`${options.provider}-sdk`};
      if(body&&!signed&&!headers.has('content-encoding')&&headers.get('content-type')?.includes('application/json')){
        const wire=parseWire(body);
        const selection=wire?selectLeaves(wire,protocol):null;
        const context=selection?await manifest(selection.context):null;
        if(selection&&context){
          const recovery=recoveryContexts.getStore();
          const bound=recovery?.runtime===options.runtime&&registered(recovery)&&options.runtime.ownsBinding(recovery.binding,options.scope)&&acceptsRecovery(wire!.value,protocol,recovery.binding)?recovery:null;
          const outcome=await options.runtime.optimize({scope:options.scope,adapter:{id:`${options.provider}-sdk`,version:'0.1.0',framework_version:options.frameworkVersion,serialization_revision:`${protocol}-wire-v1`},
            model:plain(wire!.value)&&typeof wire!.value.model==='string'?{provider:options.provider,id:wire!.value.model,protocol}:null,manifest:context,candidates:selection.leaves.map((leaf,i)=>({id:`leaf-${i}`,sourceId:JSON.stringify(leaf.path).replace(/[^a-zA-Z0-9._:/-]/g,'_'),content:leaf.value})),
            binding:bound?.binding??null,...(bound?{recoveryOverheadText:bound.overhead}:{}),logicalCallId:attempt.logicalCallId,attemptId:attempt.attemptId,
            ...(signal?{signal}:{}),});
          // A prepared replacement is associated with dispatch only after its
          // unchanged native executor is re-attested and the wire patch applied.
          attempt.optimization=outcome.replacements.length===0?outcome:null;
          if(outcome.replacements.length)attempt.reason='patch_not_applied';
          const patches=outcome.replacements.map(r=>({leaf:selection.leaves[Number(r.segment_id.slice(5))]!,replacement:r.text}));
          if(patches.length&&patches.every(p=>p.leaf)&&(!bound||registered(bound))){
            const modified=patchWire(body,patches);
            if(modified!==null){
              attempt.optimization=outcome;
              if(headers.has('content-length'))headers.set('content-length',String(new TextEncoder().encode(modified).length));
              next={...init,body:modified,headers};
            }
          }
        }
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
        const response=await withOwner(attempt,()=>options.fetch(input,next));
        if(parent)return response;
        if(!response.ok){observe(attempt,'failed');return response;}
        return observeResponse(response,attempt,signal);
      }catch(error){if(!parent)observe(attempt,signal?.aborted?'cancelled':'failed');throw error;}
    }
    return options.fetch(input,next);
  };
}
