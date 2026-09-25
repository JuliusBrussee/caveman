import { BaseChatModel, type BaseChatModelCallOptions, type BindToolsInput } from '@langchain/core/language_models/chat_models';
import type { BaseLanguageModelInput } from '@langchain/core/language_models/base';
import { BaseMessage, AIMessageChunk, HumanMessage, coerceMessageLikeToMessage, isAIMessage, isAIMessageChunk } from '@langchain/core/messages';
import { RunnableLambda, type RunnableConfig } from '@langchain/core/runnables';
import type { ChatGeneration, ChatResult } from '@langchain/core/outputs';
import { IterableReadableStream } from '@langchain/core/utils/stream';
import { prepareLangChain, langChainUsage, langChainSupported, type LangChainOptions } from './langchain.js';
import { observe, withOwner } from './common.js';

function messages(input:BaseLanguageModelInput):BaseMessage[]{
  if(typeof input==='string')return [new HumanMessage(input)];
  if(Array.isArray(input))return input.map(coerceMessageLikeToMessage);
  return input.toChatMessages();
}

/** Public BaseChatModel delegate; provider configuration lives on inner only. */
export class CavemanChatModel<Options extends BaseChatModelCallOptions=BaseChatModelCallOptions> extends BaseChatModel<Options>{
  override withStructuredOutput:BaseChatModel<Options>['withStructuredOutput'];
  constructor(readonly inner:BaseChatModel<Options>,readonly caveman:LangChainOptions){
    super({});
    this.withStructuredOutput=((...args:Parameters<BaseChatModel<Options>['withStructuredOutput']>)=>this.project().pipe(inner.withStructuredOutput(...args))) as BaseChatModel<Options>['withStructuredOutput'];
  }
  _llmType():string{return this.inner?this.inner._llmType():'caveman';}
  private project(){return RunnableLambda.from<BaseLanguageModelInput,BaseMessage[],Options>(async(input,config)=>{
    const prepared=await prepareLangChain(messages(input),this.caveman,config);
    if(prepared.attempt){const attempt=prepared.attempt;attempt.runtime.report(attempt.optimization,{reason:attempt.reason??'no_candidate',adapter:'langchain',logicalCallId:attempt.logicalCallId,attemptId:attempt.attemptId});}
    return prepared.messages;
  });}
  override bindTools(tools:BindToolsInput[],kwargs?:Partial<Options>){
    if(!this.inner.bindTools)throw new Error('Native model does not support bindTools');
    return this.project().pipe(this.inner.bindTools(tools,kwargs)) as ReturnType<NonNullable<BaseChatModel<Options>['bindTools']>>;
  }
  override async invoke(input:BaseLanguageModelInput,config?:Partial<Options>):Promise<AIMessageChunk>{
    const prepared=await prepareLangChain(messages(input),this.caveman,config as RunnableConfig|undefined);
    if(!prepared.attempt)return this.inner.invoke(input,config);
    const attempt=prepared.attempt;observe(attempt,'dispatch_intent');
    try{const response=await withOwner(attempt,()=>this.inner.invoke(prepared.messages,config));observe(attempt,'completed',isAIMessage(response)?langChainUsage(response.usage_metadata):null);return response;}
    catch(error){observe(attempt,config?.signal?.aborted?'cancelled':'failed');throw error;}
  }
  override async stream(input:BaseLanguageModelInput,config?:Partial<Options>):Promise<IterableReadableStream<AIMessageChunk>>{
    const prepared=await prepareLangChain(messages(input),this.caveman,config as RunnableConfig|undefined);
    if(!prepared.attempt)return this.inner.stream(input,config);
    const attempt=prepared.attempt;observe(attempt,'dispatch_intent');
    const stream=await withOwner(attempt,()=>this.inner.stream(prepared.messages,config));
    async function* values(){
      const iterator=stream[Symbol.asyncIterator]();let last,finished=false;
      try{for(;;){const next=await withOwner(attempt,()=>iterator.next());if(next.done){finished=true;observe(attempt,'completed',langChainUsage(last));return;}if(isAIMessageChunk(next.value)&&next.value.usage_metadata)last=next.value.usage_metadata;yield next.value;}}
      catch(error){observe(attempt,config?.signal?.aborted?'cancelled':'failed');finished=true;throw error;}
      finally{if(!finished)observe(attempt,'cancelled');await iterator.return?.();}
    }
    return IterableReadableStream.fromAsyncGenerator(values());
  }
  async _generate(input:BaseMessage[],options:this['ParsedCallOptions']):Promise<ChatResult>{
    const prepared=await prepareLangChain(input,this.caveman,options as RunnableConfig);
    const attempt=prepared.attempt;
    if(attempt)observe(attempt,'dispatch_intent');
    try{
      const result=await (attempt?withOwner(attempt,()=>this.inner.generate([prepared.messages],options as Options)):this.inner.generate([prepared.messages],options as Options));
      const generations=result.generations[0] as ChatGeneration[];
      if(attempt){const message=generations[0]?.message;observe(attempt,'completed',message&&isAIMessage(message)?langChainUsage(message.usage_metadata):null);}
      return {generations,...(result.llmOutput?{llmOutput:result.llmOutput}:{})};
    }catch(error){if(attempt)observe(attempt,'failed');throw error;}
  }
}

export function withCavemanModel<Options extends BaseChatModelCallOptions>(model:BaseChatModel<Options>,options:LangChainOptions):BaseChatModel<Options>{
  if(!langChainSupported(options.runtime)&&options.runtime.mode!=='off')options.runtime.decline('unsupported_version');
  return new CavemanChatModel(model,options);
}
