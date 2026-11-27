import path from 'node:path';
function unsupported(message){const e=new Error(message);e.code=-32601;return e;}

export class Adapter {
  constructor(config) {this.config=config;this.memory=null;this.ns=null;this.threads=new Set();this.sequence=0;this.sources=new Map();}
  async describe() {await import('@mastra/memory');await import('@mastra/libsql');if(this.config.semantic)await import('@mastra/fastembed');return {name:'mastra-memory',version:'1.38.0',capabilities:['retrieve','dump','read_only_queries'],read_only_queries:true,config:{semantic:!!this.config.semantic},limitations:['as_of filtering unsupported; createdAt is preserved; absent timestamps use 2000-01-01 UTC','history variant recalls message history without semantic ranking','retrieval follows returned SDK message order; complete dump uses paginated resource messages','real SDK and local embedding smoke pending']};}
  async reset(ns) {
    if(!this.memory) {
      const {Memory}=await import('@mastra/memory');const {LibSQLStore,LibSQLVector}=await import('@mastra/libsql');
      const storage=new LibSQLStore({id:'memeval',url:'file:'+path.join(process.env.EP,'memory.db')});
      const settings={storage,options:{lastMessages:false,semanticRecall:false,workingMemory:{enabled:false}}};
      if(this.config.semantic) {const {fastembed}=await import('@mastra/fastembed');settings.vector=new LibSQLVector({id:'memeval-vector',url:'file:'+path.join(process.env.EP,'vectors.db')});settings.embedder=fastembed;settings.options.semanticRecall={topK:20,messageRange:0};}
      this.memory=new Memory(settings);
    }
    for(const threadId of this.threads) await this.memory.deleteThread(threadId);
    this.ns=ns;this.threads.clear();this.sources.clear();this.sequence=0;this._lastTs=undefined;
  }
  // Mastra recall collapses messages that share an identical createdAt (verified: identical timestamps -> 1 result;
  // distinct -> topK). Timestamps are kept, but ties and missing timestamps get a strictly increasing ms offset
  // per namespace; disclosed in describe().limitations.
  uniqueCreatedAt(e) {const base=e.timestamp?new Date(e.timestamp).getTime():Date.UTC(2000,0,1);this._lastTs=this._lastTs===undefined?-Infinity:this._lastTs;const t=base>this._lastTs?base:this._lastTs+1;this._lastTs=t;return new Date(t);}
  async ingest(ns,events) {
    const messages=events.map(e=>{const threadId=ns+':'+(e.session_id||'history');this.threads.add(threadId);this.sources.set(e.id,e);const role=(e.role==='user'||e.role==='assistant')?e.role:'user';/* fact/observation/system -> user; disclosed */return {id:e.id,threadId,resourceId:ns,role,content:{format:2,parts:[{type:'text',text:e.content}]},createdAt:this.uniqueCreatedAt(e)};});
    await this.memory.saveMessages({messages});return {batch_seq:++this.sequence,accepted:events.length,filtered:0,failed:0};
  }
  ready() {return {visible:true,stages:[{name:'memory',status:'ok'}],warnings:[]};}
  async all(ns) {
    const rows=[];let page=0;
    while(true) {const result=await this.memory.listMessagesByResourceId({resourceId:ns,page,perPage:100});rows.push(...result.messages);if(!result.hasMore)break;page++;}
    return rows;
  }
  text(m) {if(typeof m.content==='string')return m.content;if(Array.isArray(m.content?.parts))return m.content.parts.filter(p=>p.type==='text').map(p=>p.text).join('\n');throw new Error('unsupported SDK message content');}
  async retrieve(ns,req) {
    // Point-in-time filtering is unavailable; resource/thread IDs isolate the namespace.
    // Preserve the request and disclose this limitation rather than failing retrieval.
    let rows;
    if(this.config.semantic) {rows=[];for(const threadId of this.threads){const result=await this.memory.recall({threadId,resourceId:ns,vectorSearchString:req.query,threadConfig:{semanticRecall:{topK:req.budget.top_k,messageRange:0},lastMessages:false}});rows.push(...result.messages);}}
    else rows=await this.all(ns);
    const seen=new Set(),items=[];let remaining=req.budget.max_context_chars;
    for(const m of rows){if(seen.has(m.id))continue;seen.add(m.id);if(items.length>=req.budget.top_k)break;const text=this.text(m);if([...text].length>remaining)continue;remaining-=[...text].length;items.push({id:m.id,text,score:null,source_ids:this.sources.has(m.id)?[m.id]:[],timestamp:this.sources.get(m.id)?.timestamp||''});}
    return {items};
  }
  async dump(ns) {return {items:(await this.all(ns)).map(m=>({id:m.id,text:this.text(m),timestamp:new Date(m.createdAt).toISOString(),layer:'message'}))};}
  answer() {throw unsupported('answer unsupported');}
  async close() {if(this.memory?.storage?.close)await this.memory.storage.close();this.memory=null;}
}
