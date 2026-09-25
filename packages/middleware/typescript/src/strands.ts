import { Model, Message, TextBlock, ToolResultBlock, FunctionTool, type AgentConfig, type BaseModelConfig,
  type StreamOptions, type CountTokensOptions, type ModelStreamEvent, type LocalAgent, type Plugin, type FunctionToolConfig, type Usage as NativeUsage } from '@strands-agents/sdk';
import { MiddlewareRuntime, recoveryInputSchema, recoveryToolDescription, type Candidate, type RetrieveArgs, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, withOwner, type Attempt } from './common.js';
import { adapterCompatible, frameworkVersion } from './compatibility.js';

export type StrandsScope = Scope | ((agent:LocalAgent) => Scope);
export interface StrandsOptions { runtime:MiddlewareRuntime; scope:StrandsScope }
const adapter={id:'strands',version:'0.1.0',framework_version:frameworkVersion('@strands-agents/sdk')??'unknown',serialization_revision:'strands-message-v1'};
const count=(n:unknown):number|null=>typeof n==='number'&&Number.isSafeInteger(n)&&n>=0?n:null;
function nativeUsage(value:NativeUsage|undefined):Usage|null{
  if(!value)return null;
  const input=count(value.inputTokens),output=count(value.outputTokens);
  return {provenance:'client_observed_sdk',complete:input!==null&&output!==null,input_tokens:input,output_tokens:output,
    cache_read_tokens:count(value.cacheReadInputTokens),cache_write_tokens:count(value.cacheWriteInputTokens),reasoning_tokens:null};
}

/** Native streamAggregated and agent orchestration remain in Strands. */
export class CavemanStrandsModel<T extends BaseModelConfig=BaseModelConfig> extends Model<T>{
  registration:StrandsRegistration|null=null;
  private readonly versionSupported:boolean;
  constructor(readonly inner:Model<T>,readonly options:StrandsOptions){
    super();this.versionSupported=adapterCompatible('strands');
    if(!this.versionSupported&&options.runtime.mode!=='off')options.runtime.decline('unsupported_version');
  }
  override get stateful(){return this.inner.stateful;}
  updateConfig(config:T){this.inner.updateConfig(config);}
  getConfig():T{return this.inner.getConfig();}
  override countTokens(messages:Message[],options?:CountTokensOptions){return this.inner.countTokens(messages,options);}
  private scope():Scope{
    if(typeof this.options.scope!=='function')return this.options.scope;
    const agent=this.registration?.agent?.deref();
    if(!agent)throw new Error('A dynamic Strands scope requires the native agent registration');
    return this.options.scope(agent);
  }

  private async prepare(messages:Message[],options:StreamOptions):Promise<{messages:Message[];attempt:Attempt|null}>{
    if(options.cancelSignal?.aborted&&!currentOwner())this.options.runtime.report(null,{reason:'cancelled',adapter:adapter.id});
    options.cancelSignal?.throwIfAborted();
    if(currentOwner())return {messages,attempt:null};
    const passive=(reason:string)=>({messages,attempt:{runtime:this.options.runtime,
      scope:{namespace:'caveman-report',session_id:'passive',branch_id:'main',cache_epoch:'0'},
      logicalCallId:crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,passive:true,reason,adapter:adapter.id}});
    if(this.options.runtime.mode==='off')return passive('disabled');
    if(!this.versionSupported)return passive('unsupported_version');
    if(this.stateful)return passive('opaque_context');
    const context=await manifest([{system:options.systemPrompt??null},...messages.map(m=>m.toJSON())]);
    if(!context)return passive('unsupported_shape');
    const names=new Map<string,string>();
    for(const message of messages)for(const block of message.content)if(block.type==='toolUseBlock')names.set(block.toolUseId,block.name);
    const candidates:Candidate[]=[],paths=new Map<string,{mi:number;bi:number;pi:number}>();
    messages.forEach((message,mi)=>message.content.forEach((block,bi)=>{
      if(block.type!=='toolResultBlock'||block.status==='error'||!names.has(block.toolUseId)||names.get(block.toolUseId)==='caveman_retrieve')return;
      block.content.forEach((part,pi)=>{
        if(part.type!=='textBlock'||Object.getPrototypeOf(part)?.constructor?.name!=='TextBlock')return;
        const id=`message-${mi}.block-${bi}.part-${pi}`;candidates.push({id,sourceId:id,content:part.text});paths.set(id,{mi,bi,pi});
      });
    }));
    const scope=this.scope(),runtime=this.options.runtime;
    const automatic=!options.toolChoice||'auto'in options.toolChoice;
    const binding=automatic&&this.registration?.bound(options)?runtime.recovery(scope):null;
    const attempt:Attempt={runtime,scope,logicalCallId:crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,adapter:adapter.id};
    const result=await runtime.optimize({scope,adapter,manifest:context,candidates,binding,logicalCallId:attempt.logicalCallId,attemptId:attempt.attemptId,
      model:this.inner.modelId?{provider:this.inner.constructor.name,id:this.inner.modelId,protocol:'strands'}:null,
      ...(binding?{recoveryOverheadText:JSON.stringify(this.registration!.recoveryTool.toolSpec)}:{}),...(options.cancelSignal?{signal:options.cancelSignal}:{})});
    attempt.optimization=result.replacements.length?null:result;
    if(!result.replacements.length)return {messages,attempt};
    if(!result.replacements.every(r=>paths.has(r.segment_id))){attempt.reason='invalid_plan';return {messages,attempt};}
    if(binding&&!this.registration?.bound(options)){attempt.reason='recovery_unavailable';return {messages,attempt};}
    const view=messages.slice();
    for(const replacement of result.replacements){
      const {mi,bi,pi}=paths.get(replacement.segment_id)!,message=view[mi]!,block=message.content[bi] as ToolResultBlock;
      const parts=block.content.slice();parts[pi]=new TextBlock(replacement.text);
      const blocks=message.content.slice();blocks[bi]=new ToolResultBlock({...block,content:parts});
      view[mi]=new Message({...message,content:blocks});
    }
    attempt.optimization=result;
    return {messages:view,attempt};
  }

  async *stream(messages:Message[],options:StreamOptions={}):AsyncGenerator<ModelStreamEvent>{
    const prepared=await this.prepare(messages,options),attempt=prepared.attempt;
    const iterator=attempt?withOwner(attempt,()=>this.inner.stream(prepared.messages,options)[Symbol.asyncIterator]()):this.inner.stream(prepared.messages,options)[Symbol.asyncIterator]();
    if(!attempt){yield* {[Symbol.asyncIterator]:()=>iterator};return;}
    observe(attempt,'dispatch_intent');let usage:Usage|null=null,done=false;
    try{
      while(true){
        const next=await withOwner(attempt,()=>iterator.next());
        if(next.done){done=true;observe(attempt,'completed',usage);return;}
        if(!attempt.passive&&next.value.type==='modelMetadataEvent')usage=nativeUsage(next.value.usage)??usage;
        yield next.value;
      }
    }catch(error){done=true;observe(attempt,options.cancelSignal?.aborted?'cancelled':'failed');throw error;}
    finally{if(!done)observe(attempt,'cancelled');await withOwner(attempt,()=>iterator.return?.());}
  }
}

class StrandsRegistration implements Plugin{
  readonly name='caveman:middleware';
  agent:WeakRef<LocalAgent>|null=null;
  readonly recoveryTool:FunctionTool;
  constructor(readonly options:StrandsOptions){
    this.recoveryTool=new FunctionTool({name:'caveman_retrieve',description:recoveryToolDescription,inputSchema:{...structuredClone(recoveryInputSchema),required:[...recoveryInputSchema.required]} as NonNullable<FunctionToolConfig['inputSchema']>,
      callback:async(input,context)=>{
        const scope=typeof options.scope==='function'?options.scope(context.agent):options.scope;
        return JSON.stringify(await options.runtime.retrieve(scope,input as RetrieveArgs,context.cancelSignal));
      }});
  }
  initAgent(agent:LocalAgent){
    if(this.agent&&this.agent.deref()!==agent)throw new Error('Create a separate Caveman Strands bundle for each agent');
    this.agent=new WeakRef(agent);
    // Registration runs after the host discovers its own tools. A collision
    // keeps the host's tool and prevents this model wrapper from using lossiness.
    if(this.options.runtime.mode==='compress'&&!agent.toolRegistry.get('caveman_retrieve'))agent.toolRegistry.add(this.recoveryTool);
  }
  bound(options:StreamOptions):boolean{
    const agent=this.agent?.deref(),matches=options.toolSpecs?.filter(t=>t.name==='caveman_retrieve')??[];
    return agent?.toolRegistry.get('caveman_retrieve')===this.recoveryTool&&matches.length===1&&JSON.stringify(matches[0])===JSON.stringify(this.recoveryTool.toolSpec);
  }
}

export function withCavemanStrandsModel<T extends BaseModelConfig>(model:Model<T>,options:StrandsOptions):Model<T>{
  return new CavemanStrandsModel(model,options);
}

export function withCavemanStrands(input:AgentConfig&{model:Model},options:StrandsOptions):AgentConfig{
  const model=new CavemanStrandsModel(input.model,options);
  if(options.runtime.mode==='off'||!adapterCompatible('strands'))return {...input,model};
  const registration=new StrandsRegistration(options);
  model.registration=registration;
  return {...input,model,plugins:[...(input.plugins??[]),registration]};
}
