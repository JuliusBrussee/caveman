import { createMiddleware, type AgentMiddleware } from 'langchain';
import { BaseMessage, ToolMessage, isAIMessage, type UsageMetadata } from '@langchain/core/messages';
import { tool, type ClientTool, type ServerTool } from '@langchain/core/tools';
import { ensureConfig, type RunnableConfig } from '@langchain/core/runnables';
import type { JSONSchema } from '@langchain/core/utils/json_schema';
import { BaseDocumentCompressor } from '@langchain/core/retrievers/document_compressors';
import { Document, type DocumentInterface } from '@langchain/core/documents';
import { MiddlewareRuntime, recoveryInputSchema, recoveryToolDescription, type Candidate, type RecoveryBinding, type RetrieveArgs, type Scope, type Usage } from '@caveman-ai/sdk/middleware';
import { currentOwner, manifest, observe, plain, withOwner, type Attempt } from './common.js';
import { adapterCompatible, frameworkVersion } from './compatibility.js';

export type LangChainScope = Scope | ((config: RunnableConfig) => Scope);
export interface LangChainOptions { runtime: MiddlewareRuntime; scope: LangChainScope }
export interface LangChainDocumentOptions extends LangChainOptions {
  /** The runtime-owned reader already registered by the application for this scope. */
  sourceExpansion?: RecoveryBinding;
}
export const langChainAdapter = { id:'langchain', version:'0.1.0', framework_version:frameworkVersion('langchain')??'unknown', serialization_revision:'langchain-message-v1' };
export const langChainSupported=(_runtime:MiddlewareRuntime)=>adapterCompatible('langchain');

export function scopeFromConfig(config:RunnableConfig, namespace:string):Scope{
  const c=config.configurable??{};
  if(typeof c.thread_id!=='string'||!c.thread_id)throw new Error('LangGraph middleware requires configurable.thread_id');
  if(c.caveman_branch_id!==undefined&&typeof c.caveman_branch_id!=='string')throw new Error('Invalid caveman_branch_id');
  if(c.caveman_cache_epoch!==undefined&&typeof c.caveman_cache_epoch!=='string')throw new Error('Invalid caveman_cache_epoch');
  return {namespace,session_id:c.thread_id,branch_id:c.caveman_branch_id??'main',cache_epoch:c.caveman_cache_epoch??'0'};
}
export function resolveLangChainScope(source:LangChainScope,config?:RunnableConfig):Scope{
  return typeof source==='function'?source(ensureConfig(config)):source;
}
const number=(value:unknown):number|null=>typeof value==='number'&&Number.isSafeInteger(value)&&value>=0?value:null;
export function langChainUsage(value:UsageMetadata|undefined):Usage|null{
  if(!value)return null;
  const input=number(value.input_tokens),output=number(value.output_tokens);
  return {provenance:'client_observed_sdk',complete:input!==null&&output!==null,input_tokens:input,output_tokens:output,
    cache_read_tokens:number(value.input_token_details?.cache_read),cache_write_tokens:number(value.input_token_details?.cache_creation),reasoning_tokens:number(value.output_token_details?.reasoning)};
}

/** Clone only native ToolMessage text. Other message classes stay identical. */
export async function prepareLangChain(messages:BaseMessage[], options:LangChainOptions, config?:RunnableConfig, binding?:RecoveryBinding|null, prefix:BaseMessage[]=[]):Promise<{messages:BaseMessage[];attempt:Attempt|null}>{
  const signal=config?.signal;signal?.throwIfAborted();
  if(currentOwner())return {messages,attempt:null};
  const passive=(reason:string)=>({messages,attempt:{runtime:options.runtime,scope:{namespace:'caveman-passive',session_id:'report-only',branch_id:'main',cache_epoch:'0'},logicalCallId:crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,passive:true,reason,adapter:'langchain'} satisfies Attempt});
  if(options.runtime.mode==='off')return passive('off');
  if(!langChainSupported(options.runtime))return passive('unsupported_version');
  let context;
  try{context=await manifest([...prefix,...messages].map(m=>m.toDict()));}catch{return passive('unsupported_shape');}
  if(!context)return passive('unsupported_shape');
  const scope=resolveLangChainScope(options.scope,config);
  const names=new Map<string,string>();
  for(const message of messages)if(isAIMessage(message))for(const call of message.tool_calls??[])if(call.id)names.set(call.id,call.name);
  const candidates:Candidate[]=[], setters=new Map<string,{mi:number;pi:number|null}>();
  messages.forEach((message,mi)=>{
    if(!ToolMessage.isInstance(message)||Object.getPrototypeOf(message)?.constructor?.name!=='ToolMessage'||message.name==='caveman_retrieve'||names.get(message.tool_call_id)==='caveman_retrieve'||message.status==='error')return;
    if(!message.name&&!names.has(message.tool_call_id))return;
    const add=(text:unknown,pi:number|null)=>{
      if(typeof text!=='string')return;
      const id=`message-${mi}.part-${pi??0}`;candidates.push({id,sourceId:id,content:text});setters.set(id,{mi,pi});
    };
    if(typeof message.content==='string')add(message.content,null);
    else message.content.forEach((part,pi)=>{if(plain(part)&&part.type==='text'&&!('citations'in part))add(part.text,pi);});
  });
  const attempt:Attempt={runtime:options.runtime,scope,logicalCallId:crypto.randomUUID(),attemptId:crypto.randomUUID(),optimization:null,wireSHA256:null,adapter:'langchain'};
  const optimization=await options.runtime.optimize({scope,adapter:langChainAdapter,manifest:context,candidates,binding:binding??null,
    ...(binding?{recoveryOverheadText:JSON.stringify({name:binding.name,description:binding.description,input_schema:binding.inputSchema})}:{}),
    logicalCallId:attempt.logicalCallId,attemptId:attempt.attemptId,...(signal?{signal}:{}),});
  if(!optimization.replacements.every(r=>setters.has(r.segment_id))){attempt.reason='invalid_replacement_plan';return {messages,attempt};}
  const result=messages.slice();
  for(const replacement of optimization.replacements){
    const {mi,pi}=setters.get(replacement.segment_id)!,message=result[mi] as ToolMessage;
    let content:ToolMessage['content']=replacement.text;
    if(pi!==null&&Array.isArray(message.content)){
      const parts=message.content.slice();parts[pi]={...parts[pi] as Record<string,unknown>,text:replacement.text} as typeof parts[number];content=parts;
    }
    const NativeToolMessage=Object.getPrototypeOf(message).constructor as typeof ToolMessage;
    result[mi]=new NativeToolMessage({...message,content});
  }
  attempt.optimization=optimization;
  return {messages:optimization.replacements.length?result:messages,attempt};
}

/** Low-level native middleware plus a real native recovery tool. */
export function createCavemanLangChain(options:LangChainOptions){
  const versionSupported=langChainSupported(options.runtime);
  if(!versionSupported&&options.runtime.mode!=='off')options.runtime.decline('unsupported_version');
  const recoveryTool=tool(async(input:unknown,config?:RunnableConfig)=>{
    const scope=resolveLangChainScope(options.scope,config);
    return JSON.stringify(await options.runtime.retrieve(scope,input as RetrieveArgs,config?.signal));
  // LangChain's JSON Schema validator annotates its input schema. Give the
  // native tool its own copy rather than exposing the SDK's frozen contract.
  },{name:'caveman_retrieve',description:recoveryToolDescription,schema:structuredClone(recoveryInputSchema) as JSONSchema});
  const expectedSchema=JSON.stringify(recoveryTool.schema);
  const expectedMethods={func:recoveryTool.func,invoke:recoveryTool.invoke,call:recoveryTool.call};
  const intact=()=>{
    try{return recoveryTool.name==='caveman_retrieve'&&recoveryTool.description===recoveryToolDescription
      &&recoveryTool.returnDirect===false&&recoveryTool.responseFormat==='content'
      &&JSON.stringify(recoveryTool.schema)===expectedSchema
      &&Object.entries(expectedMethods).every(([name,method])=>recoveryTool[name as keyof typeof expectedMethods]===method);
    }catch{return false;}
  };
  const middleware=createMiddleware({name:'CavemanMiddleware',
    async wrapModelCall(request,handler){
      const config=ensureConfig();
      const named=versionSupported&&options.runtime.mode==='compress'&&!currentOwner()?request.tools.filter(t=>t.name==='caveman_retrieve'):[];
      const bound=named.length===1&&named[0]===recoveryTool&&intact()&&!request.responseFormat&&(!request.toolChoice||request.toolChoice==='auto');
      const binding=bound?options.runtime.recovery(resolveLangChainScope(options.scope,config)):null;
      const prepared=await prepareLangChain(request.messages,options,config,binding,[request.systemMessage]);
      if(!prepared.attempt)return handler(request);
      const attempt=prepared.attempt;observe(attempt,'dispatch_intent');
      try{
        const response=await withOwner(attempt,()=>handler({...request,messages:prepared.messages}));
        observe(attempt,'completed',isAIMessage(response)?langChainUsage(response.usage_metadata):null);
        return response;
      }catch(error){observe(attempt,config.signal?.aborted?'cancelled':'failed');throw error;}
    },
  });
  return {middleware,recoveryTool};
}

/** Native createAgent options; collision-safe registration with no new loop. */
export function withCavemanAgent<T extends {tools?: (ClientTool|ServerTool)[];middleware?:AgentMiddleware[]}>(input:T,options:LangChainOptions):T&{tools:(ClientTool|ServerTool)[];middleware:AgentMiddleware[]}{
  const bundle=createCavemanLangChain(options),tools=[...(input.tools??[])];
  if(options.runtime.mode==='compress'&&langChainSupported(options.runtime)&&!tools.some(t=>t.name==='caveman_retrieve'))tools.push(bundle.recoveryTool);
  return {...input,tools,middleware:[...(input.middleware??[]),bundle.middleware]};
}

/** RAG-only native compressor. Source expansion is required for lossy use. */
export class CavemanDocumentCompressor extends BaseDocumentCompressor{
  constructor(private readonly options:LangChainDocumentOptions){super();}
  async compressDocuments(documents:DocumentInterface[],_query:string):Promise<DocumentInterface[]>{
    const report=(reason:string)=>this.options.runtime.report(null,{reason,adapter:'langchain-rag'});
    if(this.options.runtime.mode==='off'){report('off');return documents;}
    if(!langChainSupported(this.options.runtime)){this.options.runtime.decline('unsupported_version');report('unsupported_version');return documents;}
    // Host applications can load another copy of @langchain/core. Preserve
    // their native constructor, as the message adapter does for ToolMessage.
    if(documents.some(d=>Object.getPrototypeOf(d)?.constructor?.name!=='Document')){report('unsupported_shape');return documents;}
    const context=await manifest(documents.map(d=>({id:d.id,pageContent:d.pageContent,metadata:d.metadata})));
    if(!context){report('unsupported_shape');return documents;}
    const scope=resolveLangChainScope(this.options.scope),reader=this.options.sourceExpansion;
    const binding=this.options.runtime.ownsBinding(reader,scope)&&typeof reader.execute==='function'?reader:null;
    const result=await this.options.runtime.optimize({scope,adapter:{...langChainAdapter,id:'langchain-rag',serialization_revision:'langchain-document-v1'},manifest:context,
      candidates:documents.map((d,i)=>({id:`document-${i}`,sourceId:d.id??`document-${i}`,content:d.pageContent,kind:'artifact'})),binding});
    const replacements=new Map(result.replacements.map(r=>[r.segment_id,r.text]));
    const segments=new Set(documents.map((_document,index)=>`document-${index}`));
    if(result.replacements.some(replacement=>!segments.has(replacement.segment_id))){report('invalid_replacement_plan');return documents;}
    const projected=documents.map((d,i)=>{
      if(!replacements.has(`document-${i}`))return d;
      const NativeDocument=Object.getPrototypeOf(d).constructor as typeof Document;
      return new NativeDocument({...d,pageContent:replacements.get(`document-${i}`)!});
    });
    this.options.runtime.report(result,{adapter:'langchain-rag'});
    return projected;
  }
}

export { CavemanChatModel, withCavemanModel } from './langchain-model.js';
