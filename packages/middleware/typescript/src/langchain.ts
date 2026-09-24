import { createMiddleware, type AgentMiddleware } from 'langchain';
import { ToolMessage, isAIMessage } from '@langchain/core/messages';
import { tool, type ClientTool, type ServerTool } from '@langchain/core/tools';
import { ensureConfig, type RunnableConfig } from '@langchain/core/runnables';
import type { JSONSchema } from '@langchain/core/utils/json_schema';
import { recoveryInputSchema, recoveryToolDescription, type RetrieveArgs, type Scope } from '@caveman-ai/sdk/middleware';
import { bindRecovery, currentOwner, nameConflict, observe, withOwner } from './common.js';
import { frameworkGate, type GateReason } from './compatibility.js';
import { langChainUsage, prepareLangChain, resolveLangChainScope, type LangChainOptions } from './langchain-model.js';

// The @langchain/core-only entries live in ./langchain-model.js (`@caveman-ai/middleware/langchain-model`), which
// never loads `langchain`; this subpath adds the agent entries and still exports everything.
export * from './langchain-model.js';

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
