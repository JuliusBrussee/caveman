import { createMiddleware, type AgentMiddleware } from 'langchain';
import { ToolMessage, isAIMessage } from '@langchain/core/messages';
import { tool, type ClientTool, type ServerTool } from '@langchain/core/tools';
import { ensureConfig, type RunnableConfig } from '@langchain/core/runnables';
import type { JSONSchema } from '@langchain/core/utils/json_schema';
import { BaseDocumentCompressor } from '@langchain/core/retrievers/document_compressors';
import type { DocumentInterface } from '@langchain/core/documents';
import { recoveryInputSchema, recoveryToolDescription, type RecoveryBinding, type RetrieveArgs, type Scope } from '@caveman-ai/sdk/middleware';
import { bindRecovery, currentOwner, manifest, nameConflict, observe, withOwner } from './common.js';
import { frameworkGate, type GateReason } from './compatibility.js';
import { guard } from './guard.js';
import { langChainAdapter, langChainUsage, prepareLangChain, resolveLangChainScope, type LangChainOptions } from './langchain-model.js';

// The @langchain/core-only entries live in ./langchain-model.js (`@caveman-ai/middleware/langchain-model`), which
// never loads `langchain`; this subpath adds the agent entries and still exports everything.
export * from './langchain-model.js';

export interface LangChainDocumentOptions extends LangChainOptions {
  /** The runtime-owned reader already registered by the application for this scope. */
  sourceExpansion?: RecoveryBinding;
}
/** C11: agent entries use `langchain` + `@langchain/core`; model and document entries use `@langchain/core` only. */
export function langChainGate(options:LangChainOptions,entry:'langchain'|'langchain-core'):GateReason|null{
  return frameworkGate(entry,options,()=>typeof ToolMessage.isInstance==='function'&&(entry==='langchain-core'||typeof createMiddleware==='function'));
}

/** Low-level native middleware plus a real native recovery tool. */
export function createCavemanLangChain(options:LangChainOptions){
  const blocked=langChainGate(options,'langchain');
  const recoveryTool=tool(async(input:unknown,config?:RunnableConfig)=>{
    const scope=resolveLangChainScope(options.scope,config);
    return JSON.stringify(await options.runtime.retrieve(scope as Scope,input as RetrieveArgs,config?.signal));
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
      const named=!blocked&&options.runtime.mode==='compress'&&!currentOwner()?request.tools.filter(t=>t.name==='caveman_retrieve'):[];
      // C14: a host tool named caveman_retrieve keeps recovery off and says why.
      const conflict=named.some(t=>t!==recoveryTool)?'recovery_name_conflict':null;
      const bound=!conflict&&named.length===1&&intact()&&!request.responseFormat&&(!request.toolChoice||request.toolChoice==='auto');
      const binding=bound?bindRecovery(options.runtime,resolveLangChainScope(options.scope,config)):null;
      const prepared=await prepareLangChain(request.messages,options,config,binding,[request.systemMessage],blocked??conflict);
      if(!prepared.attempt)return handler(request);
      const attempt=prepared.attempt;observe(attempt,'dispatch_intent');
      try{
        const response=await withOwner(attempt,()=>handler({...request,messages:prepared.messages}));
        observe(attempt,'completed',isAIMessage(response)?langChainUsage(response.usage_metadata):null);
        return response;
      }catch(error){observe(attempt,config.signal?.aborted?'cancelled':'failed');throw error;}
    },
  });
  return {middleware,recoveryTool,blocked};
}

/** Native createAgent options; the LangChain entry point that compresses. No new loop. */
export function withCavemanAgent<T extends {tools?: (ClientTool|ServerTool)[];middleware?:AgentMiddleware[]}>(input:T,options:LangChainOptions):T&{tools:(ClientTool|ServerTool)[];middleware:AgentMiddleware[]}{
  const {middleware,recoveryTool,blocked}=createCavemanLangChain(options),tools=[...(input.tools??[])];
  if(options.runtime.mode==='compress'&&!blocked){
    if(tools.some(t=>t.name==='caveman_retrieve'))nameConflict(options.runtime,'langchain');
    else tools.push(recoveryTool);
  }
  return {...input,tools,middleware:[...(input.middleware??[]),middleware]};
}

/** RAG-only native compressor. Source expansion is required for lossy use. */
export class CavemanDocumentCompressor extends BaseDocumentCompressor{
  private readonly blocked:GateReason|null;
  constructor(private readonly options:LangChainDocumentOptions){super();this.blocked=langChainGate(options,'langchain-core');}
  async compressDocuments(documents:DocumentInterface[],_query:string):Promise<DocumentInterface[]>{
    const report=(reason:string)=>this.options.runtime.report(null,{reason,adapter:'langchain-rag'});
    if(this.options.runtime.mode==='off'){report('off');return documents;}
    if(this.blocked){report(this.blocked);return documents;}
    const scope=resolveLangChainScope(this.options.scope);
    if(!scope){report('recovery_unbound');return documents;}
    return guard(this.options.runtime,'langchain-rag',undefined,async()=>{
      // C6: a structural check survives minification and another installed copy of @langchain/core.
      if(documents.some(d=>!d||typeof d.pageContent!=='string'||!Object.keys(d).every(key=>['pageContent','metadata','id'].includes(key)))){report('unsupported_shape');return documents;}
      const context=await manifest(documents.map(d=>({id:d.id,pageContent:d.pageContent,metadata:d.metadata})),this.options.manifestBytes);
      const reader=this.options.sourceExpansion;
      const binding=this.options.runtime.ownsBinding(reader,scope)&&typeof reader.execute==='function'?reader:null;
      const result=await this.options.runtime.optimize({scope,adapter:{...langChainAdapter,id:'langchain-rag',serialization_revision:'langchain-document-v1'},...context,
        candidates:documents.map((d,i)=>({id:`document-${i}`,sourceId:d.id??`document-${i}`,content:d.pageContent,kind:'artifact'})),binding});
      const replacements=new Map(result.replacements.map(r=>[r.segment_id,r.text]));
      const segments=new Set(documents.map((_document,index)=>`document-${index}`));
      if(result.replacements.some(replacement=>!segments.has(replacement.segment_id))){report('invalid_replacement_plan');return documents;}
      const projected=documents.map((d,i)=>{
        if(!replacements.has(`document-${i}`))return d;
        // Host applications can load another copy of @langchain/core; keep their native constructor.
        const NativeDocument=Object.getPrototypeOf(d).constructor as new(fields:DocumentInterface)=>DocumentInterface;
        return new NativeDocument({pageContent:replacements.get(`document-${i}`)!,metadata:d.metadata,...(d.id!==undefined?{id:d.id}:{})});
      });
      this.options.runtime.report(result,{adapter:'langchain-rag'});
      return projected;
    },()=>{report('adapter_error');return documents;});
  }
}

