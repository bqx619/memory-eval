/*  */
"use strict";
const $ = s => document.querySelector(s);
function el(tag, attrs={}, ...kids){ const n=document.createElement(tag); for (const [k,v] of Object.entries(attrs)){ if (v==null) continue; if (k==="text") n.textContent=v; else if (k==="html") n.innerHTML=v; else if (k==="style" && typeof v!=="string") Object.assign(n.style,v); else n.setAttribute(k,v);} for (const c of kids.flat()){ if (c==null) continue; n.append(c.nodeType? c : document.createTextNode(String(c))); } return n; }
const fmt = (v,d=3) => v==null||Number.isNaN(v) ? "—" : Number(v).toFixed(d);
const sgn = (v,d=3) => v==null||Number.isNaN(v) ? "—" : (v>0?"+":"")+Number(v).toFixed(d);
const pct = (v,d=0) => v==null||Number.isNaN(v) ? "—" : (100*v).toFixed(d)+"%";
const int = v => v==null ? "—" : Number(v).toLocaleString("en-US");
const dur = s => s==null ? "—" : s<90 ? `${Math.round(s)} s` : s<5400 ? `${Math.round(s/60)} min` : `${Math.floor(s/3600)} h ${Math.round(s%3600/60)} m`;
const dt = s => s ? s.replace("T"," ").slice(0,16)+"Z" : "—";
const kb = b => b==null ? "—" : b>1048576 ? (b/1048576).toFixed(1)+" MB" : Math.round(b/1024)+" KB";
const shortName = n => String(n||"").replace(/-beam100k-400$/,"");
const mean = arr => arr.length ? arr.reduce((a,b)=>a+b,0)/arr.length : null;
const median = arr => { if (!arr.length) return null; const s=[...arr].sort((a,b)=>a-b), m=s.length>>1; return s.length%2 ? s[m] : (s[m-1]+s[m])/2; };
const JUDGE = "beam.llm_judge_score", EO = "beam.event_ordering.final";
const judgeMean = r => r?.metrics?.[JUDGE]?.mean ?? null;
const byJudge = (a,b) => (judgeMean(b)??-1)-(judgeMean(a)??-1);
/* embedding declared by the SUT manifest (report sut.declared.embedding, normalised by the builder into runs[].sut.embedding;
   design/p16-beam-review.md §11). Tiers order the runs overview: API embedding → local / other embedding → none.
   Older data files lack the key → hasEmbedding false → plain judge order, no group rows, no embedding line. */
const embedOf = r => r?.sut?.embedding ?? null;
const hasEmbedding = D => (D.runs||[]).some(r => r.sut && "embedding" in r.sut);
const EMBED_TIERS = [
  {key:"api", label:"API embeddings", desc:"Embeddings computed by an external service (manifest local=false)", test: e => !!e && e.local===false},
  {key:"local", label:"Local / other embeddings", desc:"Embeddings computed inside the SUT container, or specified only as a text declaration", test: e => !!e && e.local!==false},
  {key:"none", label:"No embeddings", desc:"Manifest declares no embedding model (lexical retrieval or empty system)", test: e => !e},
];
const embedTier = r => { const e = embedOf(r), i = EMBED_TIERS.findIndex(t => t.test(e)); return i<0 ? EMBED_TIERS.length : i; };
const byEmbedThenJudge = (a,b) => embedTier(a)-embedTier(b) || byJudge(a,b);
const embedText = r => { const e = embedOf(r); if (!e) return "No embeddings"; if (e.model || e.provider) return [e.model, e.dimensions!=null?`${e.dimensions} dimensions`:null, e.provider, e.local===true?"Local":e.local===false?"API":null].filter(Boolean).join(" · "); return e.declared || "Embeddings (details unspecified)"; };
const embedLine = r => embedOf(r) ? `Embedding ${embedText(r)}` : "No embeddings";
const EMBED_TIP = "SUT-declared embeddings (report sut.declared.embedding): model, dimensions, provider, and API/local. Runs group as API → local/other → none, then sort by judge mean. Groups use declarations, not run names.";
const RECALL = "beam.evidence_recall";
const recallMean = r => r?.metrics?.[RECALL]?.mean ?? null;
const hasRecall = D => (D.runs||[]).some(r => r.metrics && r.metrics[RECALL]);
const RECALL_TIP = "Evidence recall: fraction of gold events directly represented by returned items. Each item counts only its direct event; LeafMemory counts the L0 leaf\'s event, excluding summary dependency closure. Measured before reader budgeting and only on evidence-bearing questions. See citation coverage for closure accounting and the glossary for details.";
const COV = "beam.citation_coverage";
const covMean = r => r?.metrics?.[COV]?.mean ?? null;
const hasCoverage = D => (D.runs||[]).some(r => r.metrics && r.metrics[COV]);
const COV_TIP = "Citation coverage: gold fraction covered by all declared sources, including summary dependencies. It was previously labeled evidence recall. Increasing source fanout cannot lower this value, so broad closure is not comparable with direct retrieval quality. For single-source systems it matches evidence recall. Diagnostic only.";
/* fan-out companions of evidence recall (F110, design/p18-embedding-baseline.md §8): how many distinct source events the returned items cite,
   their share of the events visible at query time, and precision / F1 of those citations against gold. Older data files lack them → columns hidden. */
const IDS = "beam.source_ids_per_question", SHARE = "beam.source_ids_episode_share", PREC = "beam.evidence_precision", F1 = "beam.evidence_f1";
const idsMean = r => r?.metrics?.[IDS]?.mean ?? null, shareMean = r => r?.metrics?.[SHARE]?.mean ?? null, precMean = r => r?.metrics?.[PREC]?.mean ?? null, f1Mean = r => r?.metrics?.[F1]?.mean ?? null;
const hasFanout = D => (D.runs||[]).some(r => r.metrics && r.metrics[IDS] && r.metrics[PREC]);
const IDS_TIP = "Direct sources / question: deduplicated events directly represented by returned items; parentheses show visible-event share. Uses scored, gold-bearing questions, matching recall. LeafMemory counts L0 events only; summary dependencies belong to citation coverage.";
const PREC_TIP = "Evidence precision: |direct sources ∩ gold| / |direct sources|. Undefined zero-source cases are excluded, so n may be smaller than recall n. F1 = 2PR/(P+R). Source accounting matches recall.";

/* resume passes that replaced scored cases under a new SUT identity (memeval --resume-cases --resume-sut-change) */
const sutChanges = r => (r.resume_history||[]).map((h,i)=>({...h,i:i+1})).filter(h=>h.sut_change);
const sutChangeText = h => `Resume ${h.i}: reran ${h.cases??"?"} questions · Source snapshot ${(h.sut_change?.previous?.source_snapshot_sha256||"").slice(0,8)||"?"} → ${(h.sut_change?.current?.source_snapshot_sha256||"").slice(0,8)||"?"}`;
const SUT_CHANGE_TIP = "Explicit SUT identity change on resume (--resume-sut-change): selected scored questions are rerun and replaced under the new identity. Prior results remain in resume-history; hashes are in resume_history[].sut_change.";

const CAT_ZH = {
  information_extraction:["Information extraction","Recover specific facts stated in the conversation"],
  knowledge_update:["Knowledge update","Answer with the latest value after an update"],
  contradiction_resolution:["Contradiction resolution","Identify and reconcile contradictory statements"],
  event_ordering:["Event ordering","Recount events chronologically, with a separate beam.event_ordering.final check"],
  temporal_reasoning:["Temporal reasoning","Calculate using conversational timestamps"],
  multi_session_reasoning:["Multi-session reasoning","Combine information and calculations across sessions"],
  summarization:["Summarization","Summarize the complete project history"],
  abstention:["Abstention","Abstain when the conversation does not contain the answer"],
  preference_following:["Preference following","Honor earlier preferences in new requests"],
  instruction_following:["Instruction following","Follow earlier format and style instructions"],
};
const catLabel = c => (CAT_ZH[c]||[c])[0];
const catDesc = c => (CAT_ZH[c]||["",""])[1];

/* ---- links between the three pages ---- */
const runHref = (run, extra) => `beam-run.html?run=${encodeURIComponent(run)}${extra||""}`;
const cogletHref = (run, qid) => `beam-coglet.html?run=${encodeURIComponent(run)}&q=${encodeURIComponent(qid)}`;
function params(){ try { const u = new URLSearchParams((window.location && window.location.search) || ""); const o = {}; for (const [k,v] of u.entries()) o[k]=v; return o; } catch (e) { return {}; } }

/* ---- small building blocks ---- */
function tile(k,v,s,opts={}){ return el("div",{class:"tile"+(opts.class?" "+opts.class:""),title:opts.title}, el("div",{class:"k",text:k}), el("div",{class:"v",text:v}), s!=null? el("div",{class:"s"}, s):null); }
function kv(rows){ return rows.filter(Boolean).flatMap(([k,v]) => [el("dt",{text:k}), el("dd",{}, v)]); }
function stack(sc, planned){ const s = el("div",{class:"stack"}); const parts=[["scored",sc.scored||0],["invalid",sc.invalid||0],["error",sc.error||0]]; let used=0; for (const [k,n] of parts){ if(!n) continue; used+=n; s.append(el("span",{class:"s-"+k,style:`width:${100*n/planned}%`,title:`${k} ${n}`})); } if (planned-used>0) s.append(el("span",{class:"s-unscheduled",style:`width:${100*(planned-used)/planned}%`,title:`Not scheduled ${planned-used}`})); return s; }
/* score distribution over the planned question set: 1 / partial / 0 / invalid / error / unscheduled */
function distOf(D, run, qs){ const d = {one:0, mid:0, zero:0, invalid:0, error:0, missing:0}; for (const q of qs){ const x = D.results?.[run]?.[q.id]; if (!x){ d.missing++; continue; } if (x.status==="scored"){ const s = x.score; if (s==null) d.missing++; else if (s>=0.999) d.one++; else if (s<=0.001) d.zero++; else d.mid++; } else if (x.status==="invalid") d.invalid++; else d.error++; } return d; }
const DIST_PARTS = [["one","d-one","Score 1"],["mid","d-mid","Partial (0 < score < 1)"],["zero","d-zero","Score 0"],["invalid","d-invalid","invalid (empty reader answer)"],["error","d-error","error (execution failure)"],["missing","d-missing","Not scheduled"]];
function distBar(d, total){ const s = el("div",{class:"stack dist"}); for (const [k,cls,label] of DIST_PARTS){ const n = d[k]||0; if (!n) continue; s.append(el("span",{class:cls,style:`width:${100*n/total}%`,title:`${label} ${n}(${pct(n/total,1)})`})); } return s; }
function distLegend(){ return el("div",{class:"legend"}, ...DIST_PARTS.map(([k,cls,label]) => el("span",{}, el("span",{class:"sw "+cls}), label))); }

/* ---- per-question cell helpers ---- */
const scoreOf = (D, run, qid) => { const x = D.results?.[run]?.[qid]; return x && x.status==="scored" ? x.score : null; };
const statusOf = (D, run, qid) => { const x = D.results?.[run]?.[qid]; return x ? x.status : "missing"; };
const workOf = (D, run, qid) => D.results?.[run]?.[qid]?.work || null;
const recallOf = (D, run, qid) => { const x = D.results?.[run]?.[qid]; return x && x.status==="scored" && typeof x.evidence_recall==="number" ? x.evidence_recall : null; };
const fullRecall = (D, run, qs) => qs.filter(q => (recallOf(D, run, q.id)??0) >= 0.999).length;
const precisionOf = (D, run, qid) => { const x = D.results?.[run]?.[qid]; return x && x.status==="scored" && typeof x.evidence_precision==="number" ? x.evidence_precision : null; };
/* fan-out of one scored question with gold evidence (the builder's population for beam.source_ids_per_question); q is the question object */
const idsOf = (D, run, q) => { const x = D.results?.[run]?.[q.id]; return x && x.status==="scored" && (q.evidence_ids||[]).length && typeof x.retrieved_ids==="number" ? x.retrieved_ids : null; };
const fanoutText = (D, run, qs) => { const v = qs.map(q=>idsOf(D, run, q)).filter(x=>x!=null); if (!v.length) return "—"; const sh = qs.map(q=>{ const x = idsOf(D, run, q); return x==null||!q.cutoff ? null : x/q.cutoff; }).filter(x=>x!=null); return `${fmt(mean(v),1)}${sh.length?`(${pct(mean(sh),1)})`:""}`; };
const workRuns = D => (D.runs||[]).filter(r => r.work && r.work.episodes);
const scoreClass = s => s==null?"c-na": s>=0.999?"c-1": s<=0.001?"c-0":"c-mid";
const scoreText = s => s==null?"—":(Number.isInteger(s)?String(s):fmt(s,2));
/*  */
function cell(D, run, qid, opts={}){
  const x = D.results?.[run]?.[qid];
  if (!x) return el("span",{class:"cell c-na",title:"Not scheduled",text:"—"});
  const dot = x.work && !opts.noStage ? el("i",{class:"stg st-"+x.work.stage, title:"Loss stage:"+stageLabel(x.work.stage)}) : null;
  const lk = x.work && !opts.noLink ? el("a",{class:"cg-link",href:cogletHref(run,qid),title:"Open this question\'s coglet view",text:"Tree"}) : null; if (lk) lk.addEventListener("click", ev => ev.stopPropagation());
  if (x.status==="scored") return el("span",{}, el("span",{class:"cell "+scoreClass(x.score),text:scoreText(x.score)}), dot, lk);
  if (x.status==="invalid") return el("span",{}, el("span",{class:"cell c-inv",title:x.error_type||"",text:"Invalid"}), dot, lk);
  return el("span",{}, el("span",{class:"cell c-err",title:x.error_type||"",text:"Error"}), dot, lk);
}

/* ---- question detail: question box + one answer box per run + CogletNet work panel ---- */
function questionBox(q){
  return el("div",{class:"box"}, el("p",{class:"q",text:q.query}),
    el("div",{class:"hd"}, el("span",{class:"badge b-cls",text:catLabel(q.category)}), el("span",{class:"badge b-dim",text:`episode ${q.episode_id}`}), el("span",{class:"badge b-dim",text:`kind ${q.kind||"—"}`}), q.difficulty? el("span",{class:"badge b-dim",text:`Difficulty ${q.difficulty}`}):null, el("span",{class:"badge b-dim",text:`cutoff ${q.cutoff??"—"} events`}), q.query_point? el("span",{class:"badge b-dim",text:q.query_point}):null),
    q.rubric && q.rubric.length ? el("div",{}, el("h3",{text:`rubric(${q.rubric.length} items; aggregate ${q.aggregate==="mean"?"mean":q.aggregate||"mean"})`}), el("ol",{class:"rub"}, ...q.rubric.map(it => el("li",{}, it.text, el("span",{class:"pol",text:`${it.polarity||""}${it.weight!=null&&it.weight!==1?` · Weight ${it.weight}`:""}`}))))) : el("p",{class:"empty",text:"No rubric text (evidence-based judgment)"}),
    (q.evidence_ids||[]).length ? el("p",{class:"sub",text:"Evidence events:"+q.evidence_ids.join(", ")}) : null);
}
function answerBox(D, r, q){
  const x = D.results?.[r.name]?.[q.id];
  const box = el("div",{class:"box"}, el("h3",{}, shortName(r.name), x?.attempt && x.attempt!=="root" ? el("span",{class:"badge b-dim",style:"margin-left:6px",title:"Score comes from this resumed attempt\'s logs (per-question attempt resolution, F104)",text:`Score from ${x.attempt}`}) : null));
  if (!x){ box.append(el("p",{class:"empty",text:"Not scheduled before budget interruption"})); return box; }
  const hd = el("div",{class:"hd"}, el("span",{class:"badge b-"+(x.status==="scored"?"scored":x.status==="invalid"?"invalid":"error"),text:x.status}),
    x.status==="scored"? el("span",{class:"badge b-cls",text:`Score ${fmt(x.score,3)}`}):null,
    x.event_ordering!=null? el("span",{class:"badge b-cls",text:`Ordering check ${fmt(x.event_ordering,3)}`}):null,
    x.evidence_recall!=null? el("span",{class:"badge b-cls",title:RECALL_TIP,text:`Evidence recall ${x.evidence_hits??"?"} / ${(q.evidence_ids||[]).length}(${fmt(x.evidence_recall,2)})`}):null,
    x.evidence_recall!=null && x.retrieved_ids!=null? el("span",{class:"badge b-cls",title:IDS_TIP+"; "+PREC_TIP,text:`Direct sources ${x.retrieved_ids} items ${q.cutoff?`(of visible events ${pct(x.retrieved_ids/q.cutoff,1)})`:""} · Precision ${x.evidence_precision!=null?fmt(x.evidence_precision,2):"Undefined"}${x.evidence_f1!=null?` · F1 ${fmt(x.evidence_f1,2)}`:""}`}):null,
    x.citation_coverage!=null && x.cited_ids!=null && (x.cited_ids!==x.retrieved_ids || Math.abs(x.citation_coverage-(x.evidence_recall??0))>1e-9)? el("span",{class:"badge b-dim",title:COV_TIP,text:`Citation coverage ${x.cited_hits??"?"} / ${(q.evidence_ids||[]).length}(${fmt(x.citation_coverage,2)}) · Declared ${x.cited_ids} sources ${q.cutoff?`(of visible events ${pct(x.cited_ids/q.cutoff,1)})`:""}`}):null,
    x.reader_abstain? el("span",{class:"badge b-dim",text:"Reader abstained"}):null, x.truncated? el("span",{class:"badge b-dim",text:"Context truncated"}):null,
    x.returned_items!=null? el("span",{class:"badge b-dim",text:`Returned ${x.returned_items} items · ${int(x.returned_chars)} characters`}):(x.returned_chars!=null? el("span",{class:"badge b-dim",text:`Returned ${int(x.returned_chars)} characters`}):null),
    x.completion_tokens!=null? el("span",{class:"badge b-dim",text:`Answer ${int(x.completion_tokens)} tokens${x.finish_reason&&x.finish_reason!=="stop"?` · ${x.finish_reason}`:""}`}):null,
    x.timings? el("span",{class:"badge b-dim",text:`Ingest ${dur((x.timings.ingest_ms||0)/1000)} · Retrieve ${fmt((x.timings.query_ms||0)/1000,1)} s · Answer ${fmt((x.timings.answer_ms||0)/1000,1)} s`}):null);
  box.append(hd);
  if (x.status!=="scored"){
    const f = x.failure||{};
    box.append(el("p",{class:"note"}, el("b",{text:"Failure stage:"}), f.class||x.error_type||"Unknown", (f.native_kinds||[]).length? ` · ${f.native_kinds.join(", ")}`:""), f.message? el("pre",{text:f.message}):null, x.error_type? el("p",{class:"sub mono",text:x.error_type}):null);
  }
  if (x.answer!=null) box.append(el("h3",{text:"Reader answer"}), el("pre",{text:x.answer||"(empty)"}));
  else if (x.status==="scored") box.append(el("p",{class:"empty",text:"Answer text was not retained"}));
  if (x.items && x.items.length){
    box.append(el("h3",{text:"Judge item decisions (expectation = probability-weighted 0 / 0.5 / 1)"}),
      el("div",{class:"tbl"}, el("table",{}, el("thead",{}, el("tr",{}, el("th",{text:"#"}), el("th",{class:"num",text:"Expected"}), el("th",{class:"num",text:"Confidence"}), el("th",{text:"Probability 0 / 0.5 / 1"}))),
        el("tbody",{}, ...x.items.map((it,i)=> el("tr",{}, el("td",{text:String(i+1)}), el("td",{class:"num",text:fmt(it.expectation,3)}), el("td",{class:"num",text:fmt(it.confidence,2)}), el("td",{class:"mono sub",text:it.probabilities? Object.entries(it.probabilities).map(([k,v])=>`${k}:${v}`).join("  "):"—"})))))));
  }
  return box;
}
function detail(D, q, runs){
  const wrap = el("div"); wrap.append(questionBox(q));
  const grid = el("div",{class:"ans"}); for (const r of runs) grid.append(answerBox(D, r, q)); wrap.append(grid);
  for (const r of runs) if (r.work) wrap.append(workPanel(D, r, q, D.results?.[r.name]?.[q.id]));
  return wrap;
}

/*  */
const STAGE = {
  no_search:["No search trace","Execution failure or damaged trace; no search process recorded"],
  no_evidence:["No evidence labels","No dataset evidence events (for example abstention); evaluate the answer only"],
  not_written:["Evidence not ingested","Evidence ingestion failed, was not dispatched, or occurred after this search"],
  not_candidate:["Not a candidate","Evidence was ingested, but neither vector retrieval nor expansion selected any of its nodes"],
  capped:["Truncated before JEV","Evidence entered candidates but was truncated by candidate/evidence-expansion caps"],
  jev_rejected:["Below JEV threshold","Evidence entered candidates but JEV p_use was below threshold"],
  not_packed:["Admitted but not packed","Evidence passed JEV but was not returned due to limit or oversized context"],
  partial:["Evidence partially returned","Some evidence events were returned; the rest were lost earlier"],
  covered:["All evidence returned","At least one node for every evidence event was selected and returned to the reader"],
};
const STAGE_ORDER = ["no_search","no_evidence","not_written","not_candidate","capped","jev_rejected","not_packed","partial","covered"];
const NODE_STAGE = ["not_written","not_candidate","capped","jev_rejected","not_packed","selected"];
const OUTCOME = {selected:["o-selected","Selected result"], result_limit:["o-not_packed","Admitted, limit exceeded"], CONTEXT_BUNDLE_TOO_LARGE:["o-not_packed","Admitted, context too large"], jev_below_threshold:["o-jev_rejected","Below JEV threshold"], jev_uncertain:["o-jev_rejected","uncertain too high"], candidate_cap:["o-capped","Candidate cap reached"], evidence_cap:["o-capped","Evidence expansion cap reached"], unselected:["o-jev_rejected","Not selected"]};
const ORIGIN = {VectorHit:"Vector match", AncestorExpansion:"Ancestor expansion", EvidenceExpansion:"Evidence expansion", Unknown:"—"};
const stageLabel = s => (STAGE[s]||[s])[0];
const stageDesc = s => (STAGE[s]||["",""])[1];
const stageBadge = s => el("span",{class:"badge b-st",title:stageDesc(s)}, el("i",{class:"stg st-"+s}), stageLabel(s));
const shortId = id => id ? String(id).slice(0, 8) : "—";

/* run-level funnel: where does the evidence get lost, by category; onPick(cat, stage) filters the question table */
function funnelTable(D, r, onPick){
  const Q = D.questions||[], cats = (D.dataset?.categories||[]).map(c=>c.id), cells = D.results[r.name]||{}, withWork = Q.filter(q => cells[q.id]?.work);
  const cols = STAGE_ORDER.slice().reverse();
  const tbl = el("table",{class:"funnel"}, el("thead",{}, el("tr",{}, el("th",{text:"Category"}), ...cols.map(s=>el("th",{class:"num st",title:stageDesc(s)}, el("i",{class:"stg st-"+s,style:"margin:0 4px 0 0"}), stageLabel(s))))));
  const tb = el("tbody");
  const rowFor = (label, qs, cat) => { const tr = el("tr",{style: cat ? "" : "font-weight:600"}, el("td",{}, cat ? el("span",{}, catLabel(cat), el("div",{class:"sub mono",text:cat})) : label));
    for (const s of cols){ const sel = qs.filter(q => cells[q.id].work.stage===s); const sc = sel.map(q=>cells[q.id].score).filter(v=>typeof v==="number"); const m = mean(sc);
      const td = el("td",{class:"n",title: onPick ? "Select to filter these questions" : null}, sel.length ? String(sel.length) : el("span",{class:"c-na",text:"·"}), sel.length ? el("span",{class:"m",text: m==null ? "" : `Mean score ${fmt(m,2)}${sc.length<sel.length?` (n=${sc.length})`:""}`}) : null);
      if (sel.length && onPick) td.addEventListener("click", () => onPick(cat||"", s));
      tr.append(td); }
    return tr; };
  tb.append(rowFor("All", withWork)); for (const c of cats) tb.append(rowFor(null, withWork.filter(q=>q.category===c), c));
  tbl.append(tb); return {table: tbl, withWork};
}
/* run-level totals over questions with a search record (+ scoped block when representation is leaf_memory or tree) */
function workSummary(D, r, withWork){
  const cells = D.results[r.name]||{};
  const S = withWork.map(q=>cells[q.id].work).filter(w=>w.search);
  const om = {}; for (const w of S) for (const [k,v] of Object.entries(w.omitted||{})) om[k] = (om[k]||0)+v;
  const eps = Object.values(r.work.episodes||{}); const lv = {}, ed = {}, st = {}; let corrupt = 0, builds = 0;
  for (const e of eps){ corrupt += e.corrupt_lines||0; builds += e.builds||0; for (const [k,v] of Object.entries(e.levels||{})) lv[k]=(lv[k]||0)+v; for (const [k,v] of Object.entries(e.edges||{})) ed[k]=(ed[k]||0)+v; for (const [k,v] of Object.entries(e.states||{})) st[k]=(st[k]||0)+v; }
  const evAll = withWork.flatMap(q=>cells[q.id].work.evidence||[]); const evStage = {}; for (const e of evAll) evStage[e.stage] = (evStage[e.stage]||0)+1;
  const out = el("div");
  out.append(el("div",{class:"two"},
    el("dl",{class:"kv"}, ...kv([
      ["Evidence events (individual)", `${evAll.length} items:` + NODE_STAGE.slice().reverse().map(s => `${s==="selected"?"Selected result":stageLabel(s)} ${evStage[s]||0}`).join(", ")],
      [`Candidate omission reasons (${S.length} questions total)`, Object.entries(om).sort((a,b)=>b[1]-a[1]).map(([k,v])=>`${(OUTCOME[k]||[0,k])[1]} ${int(v)}`).join(", ") || "—"],
      ["Candidates / returns / JEV batches per question", `${fmt(mean(S.map(w=>w.candidates)),1)} / ${fmt(mean(S.map(w=>w.items)),1)} / ${fmt(mean(S.map(w=>w.jev_batches)),1)}(mean)`],
      ["Context tokens", `mean ${fmt(mean(S.map(w=>w.context_tokens).filter(v=>v!=null)),0)}` + (S.some(w=>w.degraded) ? ` · ${S.filter(w=>w.degraded).length} questions used fallback` : "")],
    ])),
    el("dl",{class:"kv"}, ...kv([
      ["Ingest", `${int(builds)} events:` + Object.entries(st).map(([k,v])=>`${k} ${v}`).join(", ")],
      ["Coglet hierarchy", Object.entries(lv).sort((a,b)=>a[0]-b[0]).map(([k,v])=>`L${k} ${int(v)}`).join(" · ")],
      ["Structural edges", Object.entries(ed).map(([k,v])=>`${k==="SummaryParent"?"Summary parent":k==="ContextFallback"?"Fallback parent":k} ${int(v)}`).join(" · ") || "None (one leaf per event)"],
      ["Damaged trace rows", corrupt ? `${corrupt} rows (${Object.entries(r.work.episodes).filter(([,e])=>e.corrupt_lines).map(([k])=>"episode "+k).join(", ")}; evaluator log redaction damaged JSON, see F92)` : "0"],
    ]))));
  /*  */
  const leaf = eps.some(e => e.representation==="leaf_memory") || S.some(w => w.representation==="leaf_memory");
  const tree = eps.some(e => e.representation==="tree") || S.some(w => w.representation==="tree");
  if (leaf || tree){
    const P = S.filter(w => w.packed), J = S.filter(w => w.jev_summary), TR = S.filter(w => w.tree);
    const tl = {}; for (const w of TR) for (const [k,v] of Object.entries(w.tree.items_by_level||{})) tl[k]=(tl[k]||0)+v;
    const treeRow = TR.length ? `Returned nodes per question ${fmt(mean(TR.map(w=>w.items)),2)} items by level (mean) ${Object.entries(tl).sort((a,b)=>a[0]-b[0]).map(([k,v])=>`L${k} ${fmt(v/TR.length,2)}`).join(" · ")||"—"} · Mean direct sources ${fmt(mean(TR.map(w=>w.tree.direct_sources).filter(v=>v!=null)),1)} events/question (L0 own events only; upper summary closure excluded) · Mean node coverage ${fmt(mean(TR.map(w=>w.tree.covered_sources).filter(v=>v!=null)),1)} events/question · With record windows ${fmt(mean(TR.map(w=>w.items ? w.tree.recorded_items/w.items : null).filter(v=>v!=null))*100,0)}% · Own-body items only (no ancestors/subtree expansion) · Passed JEV but exceeded budget ${int(om.CONTEXT_BUNDLE_TOO_LARGE||0)} items` : "No tree record";
    const lt = eps.map(e=>e.leaf_tokens).filter(t => t && typeof t.mean==="number"), ltN = lt.reduce((a,t)=>a+(t.n||0),0);
    const ltMean = ltN ? lt.reduce((a,t)=>a+t.mean*(t.n||0),0)/ltN : null, ltMax = lt.length ? Math.max(...lt.map(t=>t.max).filter(v=>typeof v==="number")) : null;
    const mbl = {}; for (const e of eps) for (const [k,v] of Object.entries(e.members_by_level||{})) mbl[k]=(mbl[k]||0)+v;
    out.append(el("div",{class:"two"},
      el("dl",{class:"kv"}, ...kv([
        tree && !leaf ? ["Returns (cross-level Tree)", treeRow] : ["Packing (LeafMemory)", P.length ? `Packed coglets per question ${fmt(mean(P.map(w=>w.packed.memories)),2)} items (visible bodies ${fmt(mean(P.map(w=>w.packed.visible_bodies)),1)}, including L0 ${fmt(mean(P.map(w=>w.packed.visible_level0)),1)}) · Passed JEV but exceeded budget ${int(om.CONTEXT_BUNDLE_TOO_LARGE||0)} items ${r.work.config?.memory_tokens!=null?` · memory_tokens budget ${int(r.work.config.memory_tokens)}`:""}` : "No packed record"],
        ["L0 embedding input", ltMean!=null ? `Mean embedding input per L0 vector point ${int(Math.round(ltMean))} tokens · Maximum ${int(ltMax)}(n=${int(ltN)}) · Variant ${r.sut?.variant||"—"}` : (tree ? `Body indexing embeds each level's own node body. Input token counts are unavailable because upstream VectorPoint has no such field · Variant ${r.sut?.variant||"—"}` : "No input_tokens record")],
        ["Three-way JEV", J.length ? `Admission rate ${fmt(mean(J.map(w=>w.jev_summary.scored ? w.jev_summary.admitted/w.jev_summary.scored : null).filter(v=>v!=null))*100,0)}%(useful ≥ ${r.work.config?.jev_threshold??"—"}) · Mean useful ${fmt(mean(J.map(w=>w.jev_summary.useful_mean).filter(v=>v!=null)),2)} · Mean uncertain ${fmt(mean(J.map(w=>w.jev_summary.uncertain_mean).filter(v=>v!=null)),2)} · Marked uncalibrated upstream` : "No three-way record"],
      ])),
      el("dl",{class:"kv"}, ...kv([
        ["scope", `${int(eps.reduce((a,e)=>a+(e.scopes||0),0))} scopes · ${int(eps.reduce((a,e)=>a+(e.snapshots||0),0))} append calls (≤ 64 events per batch, one scope per episode)`],
        ["Snapshot members (final generation)", Object.entries(mbl).sort((a,b)=>a[0]-b[0]).map(([k,v])=>`L${k} ${int(v)}`).join(" · ") || "—"],
        ["Tree view", el("span",{}, tree && !leaf ? "Question views show scope icicle trees, highlighting candidates and selections at any level. Select a node for its body, covered events, and record window; see " : "Question views show scope icicle trees with upper bars spanning covered leaves and expandable coglet level arrays; see ", el("a",{href:cogletHref(r.name, (withWork[0]||{}).id||""),text:"any question detail view"}))],
      ]))));
  }
  return out;
}

/* ---- per-question work panel: funnel box + link to the question sub-page ---- */
function workPanel(D, r, q, x){
  const w = x?.work, root = el("div",{class:"work"});
  const title = (sub) => el("h3",{}, `CogletNet work trace · ${shortName(r.name)} `, el("span",{class:"sub",text:sub}));
  if (!w){ root.append(el("div",{class:"box"}, title(""), el("p",{class:"empty",text:"No work-trace data for this question."}))); return root; }
  root.append(funnelBox(w, title));
  const leaf = w.representation==="leaf_memory", tree = w.representation==="tree";
  root.append(el("p",{class:"note"}, el("a",{class:"preset",href:cogletHref(r.name,q.id),text:"Open this question\'s coglet view →"}), tree
    ? " The detail view shows the conversation scope as an icicle tree: event bands, L0 leaf cells, and upper summary bars. Tree candidates/selections may appear at any level. Select a node for its own body (reader input), covered events, and record window. Data file "
    : leaf
    ? " The detail view shows the conversation scope as an icicle tree with event bands, L0 leaves, and upper summary bars. Retrieval, three-way JEV, and packing are overlays; select a leaf for its model-facing level array. Data file "
    : " The detail view shows the recorded coglet forest, grouped by source and level with actual parent/fallback edges. It overlays retrieval, JEV, and packing and includes reader answers, judge decisions, and structure explanations. Data file ", el("code",{text:"beam/"+w.file}), " loads in the detail view."));
  return root;
}
function funnelBox(w, title){
  const box = el("div",{class:"box"}, title(`trace ${w.trace}${w.search?"":" · No search trace"}${w.builds_before!=null?` · Ingested before search ${w.builds_before} events`:""}`));
  const hd = el("div",{class:"hd"}, el("span",{text:"Loss stage:"}), stageBadge(w.stage));
  if (w.covered) hd.append(el("span",{class:"badge b-dim",text:`Evidence returned ${w.covered[0]} / ${w.covered[1]}`}));
  if (w.search) hd.append(el("span",{class:"badge b-dim",text:`Candidate ${w.candidates} · Returned ${w.items} · JEV ${w.jev_batches} batches · Context ${int(w.context_tokens)} tokens`}));
  if (w.packed && !w.tree) hd.append(el("span",{class:"badge b-dim",text:`Packed coglets ${w.packed.memories} · Visible bodies ${w.packed.visible_bodies}(L0 ${w.packed.visible_level0} + Ancestors ${w.packed.visible_ancestors})`}));   /*  */
  if (w.tree) hd.append(el("span",{class:"badge b-dim",text:`Returned nodes ${Object.entries(w.tree.items_by_level||{}).sort((a,b)=>a[0]-b[0]).map(([k,v])=>`L${k} ${v}`).join(" · ")||"—"} · Direct sources ${int(w.tree.direct_sources)} events (L0) · Node coverage ${int(w.tree.covered_sources)} events · With record windows ${int(w.tree.recorded_items)}`}));
  if (w.jev_summary) hd.append(el("span",{class:"badge b-dim",text:`Three-way JEV: admitted ${w.jev_summary.admitted} / ${w.jev_summary.scored} · Mean useful ${fmt(w.jev_summary.useful_mean,2)} · Mean uncertain ${fmt(w.jev_summary.uncertain_mean,2)}`}));
  if (w.omitted && Object.keys(w.omitted).length) hd.append(el("span",{class:"badge b-dim",text:"Omitted:"+Object.entries(w.omitted).map(([k,v])=>`${(OUTCOME[k]||[0,k])[1]} ${v}`).join(", ")}));
  if (w.degraded) hd.append(el("span",{class:"badge b-invalid",text:"Search fallback active"}));
  if (w.warnings && w.warnings.length) hd.append(el("span",{class:"badge b-dim",text:"Warning "+w.warnings.join(", ")}));
  box.append(hd, el("p",{class:"note",text:stageDesc(w.stage) + (w.trace==="corrupt" ? ". Redaction damaged trace rows for this episode, including this search record (F92)." : "")}));
  if (w.evidence && w.evidence.length){
    const tbl = el("table",{class:"evtbl"}, el("thead",{}, el("tr",{}, ...["Evidence event","Ingest","node","Best node","Source","Level","Vector rank","Similarity","p_use","Outcome","Rank","Reached"].map(h=>el("th",{text:h})))));
    const tb = el("tbody");
    for (const e of w.evidence){ const b = e.best||{}; const o = b.outcome ? (OUTCOME[b.outcome]||["",b.outcome]) : ["","—"];
      tb.append(el("tr",{}, el("td",{}, el("code",{text:e.source_id})), el("td",{}, el("span",{class:"badge "+(e.written==="Ready"?"b-ok":"b-no"),text:e.written||"Not recorded"})), el("td",{class:"num",text:String(e.nodes)}),
        el("td",{class:"mono",text:b.node?shortId(b.node):"—"}), el("td",{text:ORIGIN[b.origin]||b.origin||"—"}), el("td",{class:"num",text:b.level!=null?`L${b.level}`:"—"}), el("td",{class:"num",text:b.vector_rank!=null?String(b.vector_rank):"—"}),
        el("td",{class:"num",text:fmt(b.similarity,3)}), el("td",{class:"num",text:fmt(b.p_use,2)}), el("td",{text:o[1]}), el("td",{class:"num",text:b.rank!=null?String(b.rank):"—"}),
        el("td",{}, el("span",{class:"badge b-st"}, el("i",{class:"stg st-"+(e.stage==="selected"?"covered":e.stage)}), e.stage==="selected"?"Selected result":stageLabel(e.stage))))); }
    tbl.append(tb); box.append(el("div",{class:"tbl"}, tbl));
    box.append(el("p",{class:"sub",text:"Dataset gold.evidence_ids are used only for post-hoc diagnostics and are invisible to the SUT and JEV. Each row shows the furthest stage reached by that evidence. Best node is the furthest-reaching node; selected summaries also count as coverage here."}));
  }
  return box;
}

/* ---- glossary section (shared by both pages) ---- */
const BEAM_METRIC_EN = {
  "beam.citation_coverage": [
    "Citation coverage",
    "Gold fraction covered by all declared sources, including summary dependency closure.",
    [
      "Wide dependency closure can increase coverage without precise direct retrieval; do not rank systems by this diagnostic.",
      "For single-source items, this matches evidence recall."
    ]
  ],
  "beam.event_ordering.f1": [
    "Event-set F1",
    "Harmonic mean of event-set precision and recall."
  ],
  "beam.event_ordering.final": [
    "Event-order final score",
    "Normalized Kendall tau-b multiplied by event-set F1."
  ],
  "beam.event_ordering.precision": [
    "Event-set precision",
    "Precision of predicted events after equivalence alignment."
  ],
  "beam.event_ordering.recall": [
    "Event-set recall",
    "Gold-event coverage after equivalence alignment."
  ],
  "beam.event_ordering.tau_b": [
    "Kendall tau-b",
    "Rank correlation with tie correction.",
    [
      "When order cannot be compared, NaN is reported as null; final is also undefined."
    ]
  ],
  "beam.event_ordering.tau_norm": [
    "Normalized order agreement",
    "Tie-corrected Kendall tau-b mapped to [0,1]."
  ],
  "beam.evidence_f1": [
    "Evidence F1",
    "Harmonic mean of direct-source evidence precision and recall; zero when both are zero.",
    [
      "Uses the precision sample set.",
      "Tree direct sources count only L0 own events; upper summary closure belongs to citation coverage."
    ]
  ],
  "beam.evidence_precision": [
    "Evidence precision",
    "Gold fraction among deduplicated events directly represented by returned items.",
    [
      "Undefined zero-source questions are excluded; n may differ from recall.",
      "Measured before reader character budgeting.",
      "Tree direct sources count only L0 own events; upper summary closure belongs to citation coverage."
    ]
  ],
  "beam.evidence_recall": [
    "Evidence recall",
    "Gold fraction directly represented by returned items; LeafMemory and Tree count only L0 own events.",
    [
      "Measured before reader budgeting, including items later dropped; this does not establish that evidence text reached the reader.",
      "Excludes unscored questions and the 45 questions without gold evidence: 40 abstention, 4 summarization, 1 preference_following.",
      "Summary dependency closure is excluded; see beam.citation_coverage."
    ]
  ],
  "beam.llm_judge_score": [
    "BEAM rubric mean",
    "Mean of rubric-item judge values 0 / 0.5 / 1 for each question."
  ],
  "beam.source_ids_episode_share": [
    "Visible-event source share",
    "Deduplicated direct sources divided by events visible at query time.",
    [
      "Uses scored, evidence-bearing questions, matching recall.",
      "Only directly represented events count; summary closure is excluded.",
      "cutoff is the number of ingested events at query time; BEAM 100K queries use the whole episode."
    ]
  ],
  "beam.source_ids_per_question": [
    "Direct sources per question",
    "Deduplicated events directly represented by returned items.",
    [
      "Uses scored, evidence-bearing questions, matching recall.",
      "Only directly represented events count; summary closure is excluded."
    ]
  ]
};
const beamMetricDisplay = g => {
  if (!/[\u3400-\u9fff]/.test(JSON.stringify(g))) return g;
  const en = BEAM_METRIC_EN[g.id];
  return {...g, name_zh:en?.[0]||g.id, definition_zh:en?.[1]||"See Original recorded content for the source definition.",
    aggregation:en?"Mean of scored, non-null values; reported by category, never pooled across benchmarks.":"See Original recorded content",
    caveats:en?(en[2]||["Parsing/judge failures are excluded from valid denominators, not scored zero. Inspect coverage."]):[], _original:g};
};

function renderGlossary(D, tbodySel, srcSel){
  const gl = (D.glossary||[]).map(beamMetricDisplay); const src = $(srcSel); if (src) src.textContent = gl.length ? `${gl.length} entries, limited to beam.* metrics used here: evaluator MetricDef exports and derived metrics. beam.evidence_recall, beam.evidence_precision, beam.evidence_f1, beam.source_ids_per_question, and beam.source_ids_episode_share use direct sources; beam.citation_coverage uses declared closure. Caveats follow each definition.` : "Glossary unavailable during build (bin/memeval results glossary unavailable)";
  const gb = $(tbodySel); if (!gb) return; gb.replaceChildren(); for (const g of gl) gb.append(el("tr",{}, el("td",{}, el("code",{text:g.id})), el("td",{text:g.name_zh||g.name||""}), el("td",{}, el("div",{text:g.definition_zh||g.definition||""}), g.formula? el("div",{class:"sub mono",text:g.formula}):null, ...(g.caveats||[]).map(c => el("div",{class:"sub",text:"· "+c})), g._original?el("details",{class:"original-record"},el("summary",{text:"Original recorded content"}),el("pre",{text:JSON.stringify(g._original,null,2)})):null), el("td",{class:"col-sec sub",text:[g.range, g.aggregation, g.higher_is_better===false?"Lower is better":""].filter(Boolean).join(" · ")})));
}
const NO_DATA = "No data: docs-site/beam/beam-100k.js missing. Generate it with scripts/build_beam_review.py (design/p16-beam-review.md).";
