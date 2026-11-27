import { writeSync } from 'node:fs';
import {errorData} from './errors.mjs';

export class Unsupported extends Error {}
export class AdapterBase {
  constructor(config) { this.config = config; }
  describe() { throw new Unsupported('describe'); }
  reset(ns) { throw new Unsupported('reset'); }
  ingest(ns, events) { throw new Unsupported('ingest'); }
  ready(ns, handle, deadline) { throw new Unsupported('ready'); }
  retrieve(ns, req) { throw new Unsupported('retrieve'); }
  answer(ns, req) { throw new Unsupported('answer'); }
  dump(ns) { throw new Unsupported('dump'); }
  close() {}
}
export class Dispatcher {
  constructor(adapter) { this.adapter=adapter; this.ns=null; this.sources=new Set(); this.handles=new Map(); }
  async call(method,p) {
    const a=this.adapter;
    if(method==='handshake') return {protocol:'paeval-v2',transport:'jsonrpc-stdio'};
    if(method==='describe') return a.describe();
    if(method==='close') {await a.close();return {};}
    if(method==='reset') {
      if(typeof p.ns!=='string'||!p.ns) throw new Error('namespace required');
      await a.reset(p.ns);this.ns=p.ns;this.sources.clear();this.handles.clear();return {};
    }
    if(this.ns===null||p.ns!==this.ns) throw new Error('namespace is not active');
    if(method==='ingest') {
      const ids=p.events.map(e=>e.id);
      if(new Set(ids).size!==ids.length||ids.some(id=>this.sources.has(id))) throw new Error('duplicate event id');
      const h=await a.ingest(p.ns,p.events);
      if(['batch_seq','accepted','filtered','failed'].some(k=>!Number.isInteger(h[k])||h[k]<0)||h.accepted+h.filtered+h.failed!==ids.length||this.handles.has(h.batch_seq)) throw new Error('ingest accounting mismatch');
      ids.forEach(id=>this.sources.add(id));this.handles.set(h.batch_seq,{...h});return h;
    }
    if(method==='ready') {if(!['batch_seq','accepted','filtered','failed'].every(k=>this.handles.get(p.handle.batch_seq)?.[k]===p.handle[k])) throw new Error('unknown handle');return a.ready(p.ns,p.handle,p.deadline);}
    if(method==='retrieve'||method==='answer') {
      const r=await a[method](p.ns,p.request);
      if(method==='retrieve') for(const i of [...(r.items??[]),...Object.values(r.tiers??{}).flat()]) {
        if(!Array.isArray(i.source_ids)||i.source_ids.some(id=>typeof id!=='string')) throw new Error('source_ids must be a list of strings');
        if(new Set(i.source_ids).size!==i.source_ids.length||i.source_ids.some(id=>!this.sources.has(id))) throw new Error('invalid source_ids: unknown/future or duplicate citation');
        // [] stays unattributed; answer-mode support belongs to the runner.
      }
      return r;
    }
    if(method==='dump') {
      const r=await a.dump(p.ns);if(new Set(r.items.map(i=>i.id)).size!==r.items.length||r.items.some(i=>typeof i.text!=='string')) throw new Error('invalid dump');
      r.items.sort((a,b)=>a.id.localeCompare(b.id,'en'));return r;
    }
    throw new Unsupported(method);
  }
}
export async function serve(adapter, maxFrame=16*1024*1024) {
  const d=new Dispatcher(adapter);
  // All JS SDK logging goes to stderr; only this transport writes the wire.
  process.stdout.write=(...args)=>process.stderr.write(...args);
  const output=r=>{let b=Buffer.from(JSON.stringify(r)+'\n');if(b.length>maxFrame)b=Buffer.from(JSON.stringify({jsonrpc:'2.0',id:r.id,error:{code:-32000,message:'response frame too large'}})+'\n');writeSync(1,b);};
  let pending=Buffer.alloc(0),closed=false;
  try {
    for await (const chunk of process.stdin) {
      pending=Buffer.concat([pending,chunk]);
      while(pending.length) {
        const i=pending.indexOf(10);
        if(i<0&&pending.length<=maxFrame) break;
        if(i<0||i+1>maxFrame) {output({jsonrpc:'2.0',id:null,error:{code:-32000,message:'frame too large'}});return;}
        const line=pending.subarray(0,i);pending=pending.subarray(i+1);let req;
        try {req=JSON.parse(line);if(req.jsonrpc!=='2.0'||!Object.hasOwn(req,'id'))throw new Error('invalid JSON-RPC request');const result=await d.call(req.method,req.params||{});output({jsonrpc:'2.0',id:req.id,result});}
        catch(e) {console.error(e);output({jsonrpc:'2.0',id:req?.id??null,error:{code:e instanceof Unsupported||e.code===-32601?-32601:-32000,message:e.message,data:errorData(e)}});}
        if(req?.method==='close'){closed=true;return;}
      }
    }
  } finally {if(!closed)await adapter.close();}
}
