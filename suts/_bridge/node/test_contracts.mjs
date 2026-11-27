// Synthetic SDK modules are created only in the supplied scratch directory.
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL,fileURLToPath} from 'node:url';
import assert from 'node:assert/strict';
import {Dispatcher} from './index.mjs';
const dir=process.argv[2];
function put(rel,text){const p=path.join(dir,rel);fs.mkdirSync(path.dirname(p),{recursive:true});fs.writeFileSync(p,text);}
function pkg(name,code){put('node_modules/'+name+'/package.json',JSON.stringify({name,type:'module',exports:'./index.mjs'}));put('node_modules/'+name+'/index.mjs',code);}
pkg('@mastra/memory',`export class Memory {
 constructor(settings){this.settings=settings;this.storage=settings.storage;this.rows=[];if(settings.vector && !settings.embedder)throw Error('embedding required');}
 async saveMessages({messages}){for(const m of messages){if(!['user','assistant'].includes(m.role)||m.content?.format!==2||!Array.isArray(m.content?.parts))throw Error('SDK requires user/assistant v2 content.parts');if(!(m.createdAt instanceof Date)||!m.threadId||!m.resourceId)throw Error('missing temporal/session fields');}this.rows.push(...messages);}
 async recall({threadId,resourceId,vectorSearchString,threadConfig}){if(!vectorSearchString||!Number.isInteger(threadConfig.semanticRecall.topK))throw Error('bad recall');return {messages:this.rows.filter(m=>m.threadId===threadId&&m.resourceId===resourceId).slice(0,threadConfig.semanticRecall.topK)};}
 async listMessagesByResourceId({resourceId,page,perPage}){const rows=this.rows.filter(m=>m.resourceId===resourceId);return {messages:rows.slice(page*perPage,(page+1)*perPage),hasMore:(page+1)*perPage<rows.length};}
 async deleteThread(threadId){this.rows=this.rows.filter(m=>m.threadId!==threadId);}
}`);
pkg('@mastra/libsql',`export class LibSQLStore{constructor({id,url}){if(!id||!url.startsWith('file:'+process.env.EP))throw Error('storage outside EP');}close(){}} export class LibSQLVector extends LibSQLStore {}`);
pkg('@mastra/fastembed',`export const fastembed={doEmbed:async()=>({embeddings:[[0,1]]})};`);
const src=path.join(path.dirname(fileURLToPath(import.meta.url)),'../../mastra/adapter.mjs');
fs.copyFileSync(src,path.join(dir,'adapter.mjs'));
const {Adapter}=await import(pathToFileURL(path.join(dir,'adapter.mjs')).href);
for(const semantic of [false,true]){
 const a=new Adapter({semantic}),d=new Dispatcher(a);
 await d.call('reset',{ns:'ns'});
 const events=Array.from({length:300},(_,i)=>({id:String(i),role:i%3===0?'fact':i%2?'assistant':'user',content:'text '+i,session_id:'s'+(i%2),timestamp:'2020-01-02T03:04:05Z'}));
 const h=await d.call('ingest',{ns:'ns',events});assert.equal(h.accepted,300);assert.equal((await d.call('ready',{ns:'ns',handle:h})).visible,true);
 const dump=await d.call('dump',{ns:'ns'});assert.equal(dump.items.length,300);assert.equal(dump.items[0].timestamp,'2020-01-02T03:04:05.000Z');
 const request={query:'text',scope:{owner:'anon'},as_of:'2020-01-01T00:00:00Z',budget:{top_k:5,max_context_chars:1000}};
 const found=await d.call('retrieve',{ns:'ns',request});assert.equal(found.items.length,5);assert(found.items.every(i=>i.source_ids.length===1));
 a.sources.clear();const unattributed=(await d.call('retrieve',{ns:'ns',request})).items[0];assert.deepEqual(unattributed.source_ids,[]);assert.equal(unattributed.status,undefined);
 await d.call('reset',{ns:'ns'});assert.equal((await d.call('dump',{ns:'ns'})).items.length,0);
 await assert.rejects(d.call('dump',{ns:'other'}));await d.call('close',{});
}
console.log('Mastra history/semantic: counts, provenance, reset, paginated dump, createdAt/threadId/resourceId PASS');

// Container DNS aliases are syntactic endpoints; isolation belongs to the episode network.
const {Adapter: HTTPAdapter}=await import('./http.mjs');
put('routes.json',JSON.stringify({version:1,describe:{name:'fixture',limitations:[]},methods:{}}));
const httpConfig={routes:path.join(dir,'routes.json')};
for(const host of ['api','api.service','127.0.0.1','[::1]','example.com']) {
 const a=new HTTPAdapter({...httpConfig,base_url:`http://${host}:80`});
 const d=a.describe();assert.equal(d.config.bridge_base_url_host,host.replace(/^\[|\]$/g,''));
 assert(d.limitations.some(s=>s.includes(d.config.bridge_base_url_host)));assert.deepEqual(d,a.describe());
}
for(const base_url of ['https://api:8888','http://user:pass@api:8888','http://@api:8888',
 'http://api:8888?q=x','http://api:8888#x','http://api:8888?','http://api:8888#','http://api','http://api:0','http://api:8888/path']) {
 assert.throws(()=>new HTTPAdapter({...httpConfig,base_url}));
}
console.log('HTTP bridge: container DNS aliases, host disclosure, invalid URL rejection PASS');

const {timeoutSeconds}=await import('./http.mjs');
for(const [config,expected] of [[{},30],[{request_timeout_s:300},295],[{request_timeout_s:10},5],
 [{request_timeout_s:10.5},5.5],[{request_timeout_s:3},1],[{request_timeout_s:10,timeout_s:1},1]]) {
 assert.equal(timeoutSeconds(config),expected);
}
for(const [config,expected] of [[{},30],[{request_timeout_s:10},5],[{request_timeout_s:3},1],
 [{request_timeout_s:10,timeout_s:1},1],[{request_timeout_s:10,timeout_s:1,ready_timeout_s:8},8]]) {
 const a=new HTTPAdapter({...httpConfig,base_url:'http://api:8888',...config});
 a.routes.methods.ready={};let now=0,calls=0;
 a.call=async()=>{calls++;now+=(expected+0.1)*1000;return {visible:false};};
 const original=Date.now;Date.now=()=>now;
 try {await assert.rejects(a.ready('test',{},null),/HTTP visibility deadline/);assert.equal(calls,1);}
 finally {Date.now=original;}
}
console.log('HTTP bridge: RPC deadline headroom, explicit timeout and ready defaults PASS');

// Governor SDK/header/body classification survives the wire and HTTP bridge.
const {errorData}=await import('./errors.mjs');
for(const reason of ['exhausted','budget']) {
 const body={error:{type:'memeval_governor',reason,retry_after_s:60}};
 for(const value of [body,JSON.stringify(body)]) {
  const e=Object.assign(Error('SDK 503 '+JSON.stringify(value)),{body:value});
  assert.equal(errorData(e).governor,reason);
  assert.equal(errorData(Error('wrapper',{cause:e})).governor,reason);
 }
 assert.equal(errorData({response:{headers:new Headers({'X-Memeval-Governor':reason})}}).governor,reason);
}
for(const message of ['503 exhausted budget','{"error":{"type":"other","reason":"budget"}}'])assert.equal(errorData(Error(message)).governor,undefined);
const http=await import('node:http');
const server=http.createServer((req,res)=>{res.writeHead(503,{'X-Memeval-Governor':'budget'});res.end('{"error":{"type":"memeval_governor","reason":"budget"}}');});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
try {
 const a=new HTTPAdapter({...httpConfig,base_url:`http://127.0.0.1:${server.address().port}`});
 await assert.rejects(a.request({path:'/memories'},{}),e=>errorData(e).governor==='budget'&&e.message.includes('memeval_governor'));
}finally{await new Promise(r=>server.close(r));}
const {spawnSync}=await import('node:child_process');
put('governor.mjs',`export class Adapter { describe(){throw Error(\`SDK 503 {'error': {'type': 'memeval_governor', 'reason': 'exhausted'}}\`);}close(){} }`);
const child=spawnSync(process.execPath,[fileURLToPath(new URL('./index.mjs',import.meta.url)),'--adapter',path.join(dir,'governor.mjs')],{input:'{"jsonrpc":"2.0","id":1,"method":"describe"}\n',encoding:'utf8'});
assert.equal(child.status,0,child.stderr);assert.equal(JSON.parse(child.stdout).error.data.governor,'exhausted');
console.log('Governor errors: SDK wrappers, HTTP headers/body and RPC error.data PASS');

// Answer-input provenance is validated independently of the runner's mode.
const {AdapterBase,Unsupported}=await import('./protocol.mjs');
class ProvenanceFixture extends AdapterBase {
 reset(){} ingest(ns,events){return {batch_seq:1,accepted:events.length,filtered:0,failed:0};}
 retrieve(ns,req){if(req.unsupported)throw new Unsupported('explicitly unsupported');return this.result;}
}
const provenance=new ProvenanceFixture({}), dispatcher=new Dispatcher(provenance);
await dispatcher.call('reset',{ns:'test'});await dispatcher.call('ingest',{ns:'test',events:[{id:'a'}]});
for(const source_ids of [[],['a']]) {
 provenance.result={items:[{text:'actual memory',source_ids}]};const before=structuredClone(provenance.result);
 assert.deepEqual(await dispatcher.call('retrieve',{ns:'test',request:{}}),before);assert.deepEqual(provenance.result,before);
}
for(const source_ids of [['unknown'],['future'],['a','a'],null,'a',[1],['a',1]])for(const tiered of [false,true]) {
 const item={text:'bad',source_ids};provenance.result={items:[{text:'unattributed',source_ids:[]}]};
 if(tiered)provenance.result.tiers={relevantBeliefs:[item]};else provenance.result.items.push(item);
 const before=structuredClone(provenance.result);
 await assert.rejects(dispatcher.call('retrieve',{ns:'test',request:{}}),/source_ids/);assert.deepEqual(provenance.result,before);
}
provenance.result={items:[{text:'missing list'}]};await assert.rejects(dispatcher.call('retrieve',{ns:'test',request:{}}),/source_ids/);
await assert.rejects(dispatcher.call('retrieve',{ns:'test',request:{unsupported:true}}),Unsupported);
console.log('Answer inputs: empty provenance retained; unknown/future/duplicate/malformed citations rejected without mutation PASS');
