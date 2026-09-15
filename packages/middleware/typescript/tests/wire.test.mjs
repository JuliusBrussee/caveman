import assert from 'node:assert/strict';
import test from 'node:test';
import { parseWire, patchWire, pathKey } from '../dist/wire.js';
import { selectLeaves } from '../dist/provider-leaves.js';

test('wire patches touch exactly selected string token; unknown bytes and escapes stay exact',()=>{
  const text='{ "model":"x", "messages" : [{"role":"assistant","tool_calls":[{"type":"function","id":"call-1","function":{"name":"read","arguments":"{}"}}]}, { "role" : "tool", "tool_call_id":"call-1", "content":"large\\nbody\\u00e9" }], "opaque" : {"unknown":1e+02,"z": [true, null, "\\u1234"]} }';
  const wire=parseWire(text);assert.ok(wire);
  const selection=selectLeaves(wire,'openai-chat');assert.equal(selection.leaves.length,1);
  const leaf=selection.leaves[0];
  const changed=patchWire(text,[{leaf,replacement:'short 🌍'}]);
  assert.equal(changed,text.slice(0,leaf.start)+'"short 🌍"'+text.slice(leaf.end));
  assert.equal(parseWire('{"messages":[],"messages":[]}'),null,'duplicate keys cannot redirect a selected patch');
  assert.equal(patchWire(text,[{leaf,replacement:'a'},{leaf,replacement:'b'}]),null,'overlapping patches reject whole plan');
});

test('native recovery, errors, reasoning, and mixed media are protected',()=>{
  const body={messages:[{role:'assistant',content:[{type:'thinking',thinking:'opaque',signature:'signed'},{type:'tool_use',id:'retrieve-1',name:'caveman_retrieve',input:{}},{type:'tool_use',id:'source-1',name:'read',input:{}},{type:'tool_use',id:'text-1',name:'read',input:{}}]},
    {role:'user',content:[{type:'tool_result',tool_use_id:'retrieve-1',content:'original'},{type:'tool_result',tool_use_id:'source-1',is_error:true,content:'error'},
      {type:'tool_result',tool_use_id:'source-1',content:[{type:'image',source:{type:'base64',data:'opaque'}},{type:'text',text:'protected'}]},
      {type:'tool_result',tool_use_id:'text-1',content:[{type:'text',text:'eligible'}]}]}]};
  const wire=parseWire(JSON.stringify(body));const selection=selectLeaves(wire,'anthropic-messages');
  assert.equal(selection.leaves.length,1);
  assert.deepEqual(selection.leaves[0].path,['messages',1,'content',3,'content',0,'text']);
  const output=JSON.parse(patchWire(JSON.stringify(body),[{leaf:selection.leaves[0],replacement:'changed'}]));
  assert.deepEqual(output.messages[0],body.messages[0]);
  assert.deepEqual(output.messages[1].content.slice(0,3),body.messages[1].content.slice(0,3));
  assert.equal(output.messages[1].content[3].content[0].text,'changed');
});

test('unmatched native tool results stay protected when call identity is hidden',()=>{
  for(const [protocol,body] of [
    ['openai-chat',{messages:[{role:'tool',tool_call_id:'hidden-call',content:'original'}]}],
    ['anthropic-messages',{messages:[{role:'user',content:[{type:'tool_result',tool_use_id:'hidden-call',content:'original'}]}]}],
  ])assert.equal(selectLeaves(parseWire(JSON.stringify(body)),protocol).leaves.length,0);
});

test('native Responses null caller metadata preserves matching while scoped callers stay protected',()=>{
  const call={type:'function_call',call_id:'read-1',name:'read_logs',arguments:'{}',id:'fc_read-1',status:'completed',caller:null,namespace:null};
  const original={input:[call,{type:'function_call_output',call_id:'read-1',output:'original'}]};
  const wire=JSON.stringify(original), selection=selectLeaves(parseWire(wire),'openai-responses');
  assert.equal(selection.leaves.length,1);
  const projected=JSON.parse(patchWire(wire,[{leaf:selection.leaves[0],replacement:'short'}]));
  assert.deepEqual(projected.input[0],call);
  for(const extra of [{caller:{type:'code_interpreter'}},{namespace:'remote_tools'},{namespace:''},{caller:false},{unexpected:null}]){
    const body={input:[{...call,...extra},original.input[1]]};
    assert.equal(selectLeaves(parseWire(JSON.stringify(body)),'openai-responses').leaves.length,0);
  }
});
