import type { Usage } from '@caveman-ai/sdk/middleware';
import { observe, plain, type Attempt } from './common.js';

const measured = (value: unknown): number | null => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
class UsageReader {
  private text = '';
  private readonly decoder = new TextDecoder();
  private values: Record<string,unknown> = {};
  private terminal = false;
  private overflow = false;
  constructor(private readonly sse: boolean) {}
  feed(bytes: Uint8Array): void {
    if (this.overflow) return;
    this.text += this.decoder.decode(bytes,{stream:true});
    if (this.text.length > (this.sse ? 262144 : 2<<20)) { this.text='';this.overflow=true;return; }
    if (this.sse) {
      for (;;) {
        const newline=this.text.indexOf('\n'); if(newline<0)break;
        const line=this.text.slice(0,newline).trimEnd();this.text=this.text.slice(newline+1);
        if(!line.startsWith('data:'))continue;
        const data=line.slice(5).trim();
        if(data==='[DONE]'){this.terminal=true;continue;}
        try{this.event(JSON.parse(data));}catch{/* unknown events do not affect delivery */}
      }
    }
  }
  private event(value: unknown): void {
    if(!plain(value))return;
    const nested=plain(value.response)?value.response:plain(value.message)?value.message:value;
    if(plain(nested.usage))this.values={...this.values,...nested.usage};
    if(plain(value.usageMetadata))this.values={...this.values,...value.usageMetadata};
    if(value.type==='message_stop'||value.type==='response.completed'||(Array.isArray(value.choices)&&value.choices.some(c=>plain(c)&&c.finish_reason!=null)))this.terminal=true;
  }
  finish():Usage|null{
    if(this.overflow)return null;
    if(!this.sse){try{this.event(JSON.parse(this.text));this.terminal=true;}catch{return null;}}
    const u=this.values;
    const input=measured(u.input_tokens??u.prompt_tokens??u.promptTokenCount);
    const output=measured(u.output_tokens??u.completion_tokens??u.candidatesTokenCount);
    if(!Object.keys(u).length)return null;
    const inputDetail=plain(u.input_tokens_details)?u.input_tokens_details:plain(u.prompt_tokens_details)?u.prompt_tokens_details:{};
    const outputDetail=plain(u.output_tokens_details)?u.output_tokens_details:plain(u.completion_tokens_details)?u.completion_tokens_details:{};
    return {provenance:'client_observed_sdk',complete:this.terminal&&input!==null&&output!==null,input_tokens:input,output_tokens:output,
      cache_read_tokens:measured(u.cache_read_input_tokens??inputDetail.cached_tokens??u.cachedContentTokenCount),
      cache_write_tokens:measured(u.cache_creation_input_tokens),reasoning_tokens:measured(outputDetail.reasoning_tokens??u.thoughtsTokenCount)};
  }
}

function metadata(response:Response, original:Response):Response{
  for(const key of ['url','redirected','type'] as const)Object.defineProperty(response,key,{value:original[key]});
  Object.defineProperty(response,'clone',{value:()=>metadata(Response.prototype.clone.call(response),original)});
  return response;
}

/** Same native Response contract and bytes; bounded accounting occurs on pulls. */
export function observeResponse(response:Response,attempt:Attempt,signal?:AbortSignal|null):Response{
  const contentType=response.headers.get('content-type')??'';
  if(Object.getPrototypeOf(response)!==Response.prototype||!response.body||(!contentType.includes('json')&&!contentType.includes('text/event-stream')))return response;
  const parser=new UsageReader(contentType.includes('text/event-stream'));
  const reader=response.body.getReader();let ended=false;
  const end=(event:'completed'|'failed'|'cancelled')=>{
    if(ended)return;ended=true;
    observe(attempt,event,event==='completed'?parser.finish():null);
    reader.releaseLock();
  };
  const stream=new ReadableStream<Uint8Array>({
    async pull(controller){
      try{
        const next=await reader.read();
        if(next.done){end(signal?.aborted?'cancelled':'completed');controller.close();return;}
        parser.feed(next.value);controller.enqueue(next.value);
      }catch(error){end(signal?.aborted?'cancelled':'failed');controller.error(error);}
    },
    async cancel(reason){try{await reader.cancel(reason);}finally{end('cancelled');}},
  },{highWaterMark:0});
  return metadata(new Response(stream,{status:response.status,statusText:response.statusText,headers:response.headers}),response);
}
