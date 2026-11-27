import http from 'node:http';
import fs from 'node:fs';
import {AdapterBase, Unsupported} from './protocol.mjs';

export function timeoutSeconds(config) {
 return config.timeout_s??(config.request_timeout_s==null?30:Math.max(1,config.request_timeout_s-5));
}

class MissingPath extends Error {}
export function select(value,path) {
 if(path==='$'||path==='')return value;
 if(!/^\$\.[A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*|\[\d+\])*$/.test(path))throw Error('unsupported JSONPath');
 for(const m of path.slice(2).matchAll(/(?:^|\.)([A-Za-z_][\w-]*)|\[(\d+)\]/g)){const key=m[1]??Number(m[2]);if(value===null||typeof value!=='object')throw Error('invalid JSONPath value');if(!Object.hasOwn(value,key))throw new MissingPath('missing JSONPath');value=value[key];}
 return value;
}
export function render(value,slots) {
 if(Array.isArray(value))return value.map(v=>render(v,slots));
 if(value&&typeof value==='object') {if(Object.keys(value).length===1&&Object.hasOwn(value,'$'))return select(slots,'$.'+value.$);return Object.fromEntries(Object.entries(value).map(([k,v])=>[k,render(v,slots)]));}
 return value;
}
export class Adapter extends AdapterBase {
 constructor(config) {
  super(config);this.routes=JSON.parse(fs.readFileSync(config.routes,'utf8'));if(this.routes.version!==1)throw Error('HTTP routes version 1 required');this.seq=0;
  this.headers={'Content-Type':'application/json'};const keys=config.headers_env??[];if(keys.length>1)throw Error('generic bearer bridge accepts at most one headers_env key; use adapter override');
  if(keys.length&&process.env[keys[0]])this.headers.Authorization='Bearer '+process.env[keys[0]];
  if(/[\s?#]/.test(config.base_url))throw Error('HTTP bridge base_url must not contain whitespace, query or fragment');
  if(config.base_url.startsWith('http://unix:')) {this.socketPath=config.base_url.slice('http://unix:'.length);if(!this.socketPath.startsWith('/'))throw Error('absolute Unix socket required');}
  else {const u=new URL(config.base_url);const authority=config.base_url.split('/')[2]??'';if(/\s/.test(config.base_url)||u.protocol!=='http:'||!u.hostname||!/:([0-9]+)$/.test(authority)||authority.includes('@')||u.username||u.password||u.pathname!=='/'||/[?#]/.test(config.base_url)||!/^\[?[A-Za-z0-9.:-]+\]?$/.test(u.hostname))throw Error('HTTP bridge requires http://host:port without userinfo, query or fragment');this.hostname=u.hostname.replace(/^\[|\]$/g,'');this.port=Number(u.port||80);if(this.port<1||this.port>65535)throw Error('invalid HTTP port');}
 }
 describe(){const d=structuredClone(this.routes.describe);const host=this.hostname??'unix';d.limitations??=[];d.limitations.push('HTTP bridge host='+host+'; address syntax only, network isolation is enforced by the container');d.config={...d.config,bridge_base_url_host:host};return d;}
 async request(route,slots) {
  let path=route.path.replace(/\{([A-Za-z_][\w.]*)\}/g,(_,p)=>encodeURIComponent(String(select(slots,'$.'+p))));if(!path.startsWith('/')||path.startsWith('//'))throw Error('local route required');
  const query=new URLSearchParams(Object.fromEntries(Object.entries(render(route.query??{},slots)).filter(([,v])=>v!==null))).toString();if(query)path+=(path.includes('?')?'&':'?')+query;
  const data=Object.hasOwn(route,'body')?JSON.stringify(render(route.body,slots)):undefined;
  return new Promise((resolve,reject)=>{
   const req=http.request({socketPath:this.socketPath,hostname:this.hostname,port:this.port,path,method:route.method??'POST',headers:this.headers,timeout:timeoutSeconds(this.config)*1000},res=>{
    const chunks=[];let size=0;res.on('data',chunk=>{size+=chunk.length;if(size>16*1024*1024){req.destroy(Error('HTTP response frame too large'));return;}chunks.push(chunk);});res.on('error',reject);
    res.on('end',()=>{if(res.statusCode<200||res.statusCode>=300){const e=Error('local HTTP status '+res.statusCode+': '+Buffer.concat(chunks).toString());e.body=Buffer.concat(chunks).toString();e.headers=res.headers;reject(e);return;}try {const raw=Buffer.concat(chunks).toString();resolve(raw?JSON.parse(raw):{});}catch(e){reject(e);}});
   });req.on('timeout',()=>req.destroy(Error('HTTP timeout')));req.on('error',reject);req.end(data);
  });
 }
 async call(name,slots) {
  const route=this.routes.methods[name];if(!route)throw new Unsupported(name);
  const p=route.pagination;slots={...slots,page:0,offset:0,cursor:null,limit:p?(p.limit??100):0};const items=[],seen=new Set();
  for(let page=0;page<(p?(p.max_pages??100):1);page++) {
   slots.page=page;slots.offset=page*slots.limit;const response=await this.request(route,slots);
   if(!Object.hasOwn(route,'items'))return render(route.response??{$:'response'},{...slots,response});
   const got=select(response,route.items);if(!Array.isArray(got))throw Error('HTTP items must be a list');
   for(const item of got) {
    const mapped=Object.fromEntries(Object.entries(route.map).map(([k,v])=>{
     try{const value=select(item,v);return [k,k==='source_ids'&&value===null?[]:value];}
     catch(e){if(k==='source_ids'&&e instanceof MissingPath)return [k,[]];throw e;}
    }));
    if(typeof mapped.id!=='string'||seen.has(mapped.id))throw Error('duplicate/invalid HTTP memory id');
    seen.add(mapped.id);if(typeof mapped.source_ids==='string')mapped.source_ids=[mapped.source_ids];items.push(mapped);
   }
   if(!p)return {items};
   if((p.kind??'offset')==='cursor') {slots.cursor=select(response,p.next);if(slots.cursor===null||slots.cursor==='')return {items};}
   else if(got.length<slots.limit)return {items};
  }
  if(p)throw Error('HTTP pagination exhausted; refusing partial result');return {items};
 }
 async reset(ns){await this.call('reset',{ns});this.seq=0;}
 async ingest(ns,events){let r={};if(this.routes.methods.ingest.foreach==='events'){for(const event of events)await this.call('ingest',{ns,event,events});}else r=await this.call('ingest',{ns,events});return {batch_seq:++this.seq,accepted:r.accepted??events.length,filtered:r.filtered??0,failed:r.failed??0};}
 async ready(ns,handle,deadline){if(!this.routes.methods.ready){if(this.routes.ready_strategy!=='sync')throw new Unsupported('ready: no verified visibility strategy');return {visible:true,stages:[],warnings:[]};}const end=Math.min(Date.now()+(this.config.ready_timeout_s??timeoutSeconds(this.config))*1000,deadline?Date.parse(deadline):Infinity);for(;;){const r=await this.call('ready',{ns,handle,deadline});if(r.visible===true)return {...r,visibility:'weak'};if(Date.now()>=end)throw Error('HTTP visibility deadline');await new Promise(r=>setTimeout(r,50));}}
 async retrieve(ns,request){const r=await this.call('retrieve',{ns,request});let left=request.budget.max_context_chars;const items=[];for(const item of r.items){if(items.length>=request.budget.top_k)break;const n=Array.from(item.text).length;if(n>left)continue;left-=n;items.push(item);}return {items};}
 answer(ns,request){return this.call('answer',{ns,request});}
 dump(ns){return this.call('dump',{ns});}
}
