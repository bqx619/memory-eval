/*  */
(function(){
"use strict";
const SVGNS = "http://www.w3.org/2000/svg";
function el(tag, attrs={}, ...kids){ const n = document.createElement(tag); for (const [k,v] of Object.entries(attrs)){ if (v==null) continue; if (k==="text") n.textContent = String(v); else if (k==="html") n.innerHTML = v; else if (k==="class") n.className = v; else n.setAttribute(k, String(v)); } for (const c of kids.flat()){ if (c!=null) n.append(c); } return n; }
function svgEl(tag, attrs={}, ...kids){ const n = document.createElementNS(SVGNS, tag); for (const [k,v] of Object.entries(attrs)){ if (v==null) continue; if (k==="text") n.textContent = String(v); else n.setAttribute(k, String(v)); } for (const c of kids.flat()){ if (c!=null) n.append(c); } return n; }
const fmt = (v, d=2) => (typeof v==="number" && isFinite(v)) ? v.toFixed(d) : "—";
const int = v => (typeof v==="number" && isFinite(v)) ? v.toLocaleString("en-US") : "—";
const shortId = s => s==null ? "—" : String(s).slice(0, 8);
const chW = ch => /[^\x00-\xff]/.test(ch) ? 9.6 : /[A-Z]/.test(ch) ? 6.2 : /[mw]/.test(ch) ? 7.6 : /[ilj.,:;'!| ]/.test(ch) ? 2.9 : 5.3;
function fit(t, px){ let w = 0, out = ""; for (const ch of String(t||"")){ w += chW(ch); if (w > px) return out.replace(/\s+$/,"")+"…"; out += ch; } return out; }
function firstLine(t){ if (t==null) return "(no body)"; for (const raw of String(t).split("\n")){ const ln = raw.replace(/^[\s#>*\-`|\/]+/, "").replace(/[`*_]/g,"").replace(/\s+/g," ").trim(); if ((ln.match(/[\p{L}\p{N}]/gu)||[]).length >= 3) return ln; } return String(t).replace(/\s+/g," ").trim() || "(blank)"; }
const mean = a => a.length ? a.reduce((x,y)=>x+y,0)/a.length : null;

/*  */
const OUT2STATE = {selected:"S", result_limit:"pass", CONTEXT_BUNDLE_TOO_LARGE:"pass", jev_below_threshold:"low", jev_uncertain:"low", candidate_cap:"cap", evidence_cap:"cap"};
const STATE_ORDER = ["none","R","cap","low","pass","S"];
const STATE_ZH = {S:"Selected S (packed into context)", pass:"Passed JEV but not packed (budget)", low:"JEV not admitted", cap:"Candidate truncated", R:"Candidate not scored", none:"Not a candidate"};
const OUTCOME_ZH = {selected:"Selected", result_limit:"Limit exceeded", CONTEXT_BUNDLE_TOO_LARGE:"Context bundle too large (memory_tokens budget)", jev_below_threshold:"useful below threshold", jev_uncertain:"uncertain too high; not admitted", unselected:"Candidate", candidate_cap:"Candidate cap exceeded", evidence_cap:"Evidence cap exceeded"};
const ROLE_ZH = {user:"User", assistant:"Assistant", system:"System", tool:"Tool"};
function stateOf(c, thr){
  if (!c) return "none";
  if (c.outcome==="selected" || c.rank!=null) return "S";
  if (OUT2STATE[c.outcome]) return OUT2STATE[c.outcome];
  const u = c.jev?.useful ?? c.p_use;
  if (u!=null && thr!=null) return u >= thr ? "pass" : "low";
  return "R";
}

/*  */
function model(W, q, x, s){
  const T = h => (h && W.texts && W.texts[h]!==undefined) ? W.texts[h] : null;
  const builds = W.builds||[], before = s?.builds_before ?? builds.length;
  let opId = null; for (let i = Math.min(before, builds.length)-1; i >= 0; i--){ if (builds[i]?.operation_id){ opId = builds[i].operation_id; break; } }
  let scopeId = null, scope = null, snap = null;
  for (const [sid, sc] of Object.entries(W.scopes||{})) for (const sn of sc.snapshots||[]) if (opId!=null && sn.operation_id===opId){ scopeId = sid; scope = sc; snap = sn; }
  if (!snap) for (const [sid, sc] of Object.entries(W.scopes||{})) for (const sn of sc.snapshots||[]) if ((sn.first_seq??0) < before && (!snap || (sn.generation??0) > (snap.generation??0))){ scopeId = sid; scope = sc; snap = sn; }
  const nodes = new Map();
  for (const m of snap?.members||[]){ const n = scope?.nodes?.[m.node]; if (!n) continue; nodes.set(m.node, {...n, id:m.node, edge:m.edge??n.edge??null, ordinal:m.ordinal??n.ordinal_first??n.ordinal??null}); }
  const leaves = [...nodes.values()].filter(n=>(n.level??0)===0 && Array.isArray(n.leaf_range)).sort((a,b)=>a.leaf_range[0]-b.leaf_range[0]);
  const uppers = [...nodes.values()].filter(n=>(n.level??0)>0 && Array.isArray(n.leaf_range)).sort((a,b)=>a.level-b.level || a.leaf_range[0]-b.leaf_range[0]);
  const N = Math.max(1, ...leaves.map(n=>n.leaf_range[1]), ...uppers.map(n=>n.leaf_range[1]));
  const maxLevel = Math.max(0, ...uppers.map(n=>n.level));
  const events = (snap?.events||[]).slice().sort((a,b)=>(a.event_sequence??0)-(b.event_sequence??0));
  const evBySource = new Map(events.map(e=>[e.source_id, e])), evByEvent = new Map(events.map(e=>[e.event_id, e]));
  const srcOf = n => n?.origin ? (n.origin.source_id ?? evByEvent.get(n.origin.event_id)?.source_id ?? null) : null;
  const thr = s?.jev_threshold ?? W.config?.jev_threshold ?? W.build_config?.jev_threshold ?? null;
  const cand = new Map();
  for (const c of s?.candidates||[]) cand.set(c.node, {...c});
  for (const it of s?.items||[]){ const c = cand.get(it.node); if (c) Object.assign(c, {rank:it.rank, outcome:"selected", item:it, rank_utility:it.rank_utility, jev:it.jev??c.jev}); else cand.set(it.node, {...it, outcome:"selected", item:it}); }
  for (const [node, reason] of Object.entries(s?.omitted||{})){ const c = cand.get(node); if (c){ if (!c.outcome) c.outcome = reason; } else cand.set(node, {node, level:0, origin:"Unknown", outcome:reason, implicit:true}); }
  const jevT = new Map(); for (const b of s?.jev_trace||[]) for (const t of b.targets||[]) jevT.set(t.node, {...t, batch:b.batch, model:b.model, attempts:b.attempts, error_code:b.error_code, output_format:b.output_format});
  const evidence = new Set(q?.evidence_ids||[]);
  for (const n of leaves) if (n.origin && n.origin.source_id==null) n.origin = {...n.origin, source_id: srcOf(n)};
  const evLeaves = new Set(leaves.filter(n => evidence.has(srcOf(n))).map(n=>n.id));
  const cvMem = new Map(); for (const m of s?.context_view?.memories||[]) cvMem.set(m.node, m);
  /*  */
  const tree = W.representation==="tree";
  const cvNodes = new Map(); for (const n of s?.context_view?.nodes||[]) if (n?.node!=null) cvNodes.set(n.node, n);
  const evIdx = new Set(leaves.filter(n=>evLeaves.has(n.id)).map(n=>n.leaf_range[0]));
  const evNodes = new Set(evLeaves); if (tree) for (const n of uppers) for (let i = n.leaf_range[0]; i < n.leaf_range[1]; i++) if (evIdx.has(i)){ evNodes.add(n.id); break; }
  const M = {W, q, x, s, T, thr, scopeId, scope, snap, nodes, leaves, uppers, N, maxLevel, events, evBySource, evByEvent, srcOf, cand, jevT, evidence, evLeaves, evNodes, cvMem, cvNodes, tree, hl:null, sel:null, ndEl:new Map(), evEl:new Map()};
  M.leafIndex = new Map(leaves.map(n=>[n.id, n.leaf_range[0]]));
  return M;
}

/*  */
function stats(M){
  const s = M.s, cands = [...M.cand.values()], scored = cands.filter(c=>c.jev || c.p_use!=null);
  const useful = c => c.jev?.useful ?? c.p_use;
  const admitted = scored.filter(c => M.thr!=null && useful(c) >= M.thr);
  const S = s?.items||[], om = {}; for (const c of cands) if (c.outcome && c.outcome!=="selected" && c.outcome!=="unselected") om[c.outcome] = (om[c.outcome]||0)+1;
  const choice = {}; for (const t of M.jevT.values()) if (t.provider?.choice) choice[t.provider.choice] = (choice[t.provider.choice]||0)+1;
  const jt = s?.jev_trace||[], failedAttempts = jt.reduce((a,b)=>a+(b.attempts||[]).filter(t=>t.error_code||t.model_failure).length,0);
  const lv = {}; for (const n of M.nodes.values()) lv[n.level??0] = (lv[n.level??0]||0)+1;
  const fb = [...M.nodes.values()].filter(n=>n.edge?.kind==="ContextFallback").length;
  const toks = M.leaves.map(n=>n.input_tokens).filter(v=>typeof v==="number");
  const selToks = S.map(it => M.nodes.get(it.node)?.input_tokens).filter(v=>typeof v==="number");
  const itemLevels = {}; for (const it of S){ const l = it.level ?? M.nodes.get(it.node)?.level ?? 0; itemLevels[l] = (itemLevels[l]||0)+1; }
  const candLevels = {}; for (const c of cands){ const l = c.level ?? M.nodes.get(c.node)?.level ?? 0; candLevels[l] = (candLevels[l]||0)+1; }
  const recordedN = S.filter(it => it.recorded && it.recorded[0]!=null).length;
  const coveredSources = S.reduce((a,it)=>a+(Array.isArray(it.sources)?it.sources.length:(it.reader_sources_n||0)),0);
  //
  const directSources = S.some(it=>it.level!=null) ? new Set(S.filter(it=>it.level===0).flatMap(it=>Array.isArray(it.sources)?it.sources:[])).size : coveredSources;
  const nodeTok = {}; for (const n of M.nodes.values()){ if (typeof n.input_tokens!=="number") continue; const l = n.level??0; (nodeTok[l] = nodeTok[l]||[]).push(n.input_tokens); }
  const nodeTokens = Object.fromEntries(Object.entries(nodeTok).map(([l,a])=>[l,{n:a.length, mean:mean(a), max:Math.max(...a)}]));
  const snaps = M.scope?.snapshots||[], seen = snaps.filter(sn => (sn.first_seq??0) < (s?.builds_before ?? Infinity));
  const calls = seen.reduce((a,sn)=>a+(sn.calls?.n||0),0), wall = seen.reduce((a,sn)=>a+(sn.calls?.wall_ms||0),0), failedOps = seen.filter(sn=>sn.error).length;
  return {cands:cands.length, scored:scored.length, admitted:admitted.length, S:S.length, om, choice, batches:jt.length, failedAttempts,
    usefulMean:mean(scored.map(useful).filter(v=>typeof v==="number")), uncertainMean:mean(scored.map(c=>c.jev?.uncertain).filter(v=>typeof v==="number")),
    levels:lv, fallback:fb, leafTokens:{mean:mean(toks), max:toks.length?Math.max(...toks):null}, selTokens:selToks, itemLevels, candLevels, recordedN, directSources, coveredSources, nodeTokens,
    packed:s?.packed||null, ctxTokens:s?.context_view?.tokens??null, settings:s?.settings||{}, snapshots:seen.length, events:M.events.length, calls, wallMin: wall ? wall/60000 : null, failedOps,
    admittedSet:new Set(admitted.map(c=>c.node)), scoredSet:new Set(scored.map(c=>c.node)), selectedSet:new Set(S.map(i=>i.node)), candSet:new Set(cands.map(c=>c.node))};
}

/*  */
function threeClassBar(jev, opts={}){
  const box = el("span",{class:"ct-tcb"+(opts.compact?" compact":"")});
  if (!jev || typeof jev.useful!=="number"){ box.append(el("span",{class:"ct-tcb-na",text:"No three-way probabilities"})); return box; }
  const bar = el("span",{class:"bar"}); for (const [k,cls] of [["useful","u"],["uncertain","q"],["not_useful","n"]]){ const v = Math.max(0, Math.min(1, jev[k]??0)); bar.append(el("i",{class:cls, style:`width:${(v*100).toFixed(1)}%`, title:`${k} ${fmt(v,2)}`})); }
  box.append(bar);
  if (!opts.compact) box.append(el("span",{class:"lbl",text:`useful ${fmt(jev.useful,2)} · uncertain ${fmt(jev.uncertain,2)} · not_useful ${fmt(jev.not_useful,2)}`}));
  return box;
}

/*  */
const CELL = 14, RH = 34, GAP = 6, EVH = 20, AXH = 16, PAD = 6, LBL = 34;
function icicle(container, M, opts={}){
  ensureStyles();
  const rows = M.maxLevel+1, W = PAD*2 + M.N*CELL, H = PAD + rows*(RH+GAP) + EVH + 4 + AXH + PAD;
  const yOf = l => PAD + (M.maxLevel - l)*(RH+GAP);
  const scroller = el("div",{class:"ct-scroll"}), inner = el("div",{class:"ct-inner"});
  const lbls = el("div",{class:"ct-lbls", style:`height:${H}px`});
  for (let l = M.maxLevel; l >= 0; l--) lbls.append(el("span",{class:"ct-lbl", style:`top:${yOf(l)}px;height:${RH}px`, text:"L"+l}));
  lbls.append(el("span",{class:"ct-lbl ev", style:`top:${yOf(0)+RH+GAP}px;height:${EVH}px`, text:"events"}));
  const svg = svgEl("svg",{viewBox:`0 0 ${W} ${H}`, width:W, height:H, class:"ct-svg", role:"img", "aria-label":"Scope coglet tree"});
  svg.append(svgEl("defs",{}, svgEl("pattern",{id:"ct-hatch", width:6, height:6, patternUnits:"userSpaceOnUse", patternTransform:"rotate(45)"}, svgEl("rect",{width:6, height:6, class:"ph-bg"}), svgEl("rect",{width:2, height:6, class:"ph-fg"})),
    svgEl("marker",{id:"ct-m-fb", viewBox:"0 0 8 8", refX:7, refY:4, markerWidth:6, markerHeight:6, orient:"auto"}, svgEl("path",{d:"M0,0 L8,4 L0,8 z", class:"m-fb"}))));
  const eg = svgEl("g",{class:"ct-events"}), ug = svgEl("g",{class:"ct-uppers"}), lg = svgEl("g",{class:"ct-leaves"}), fg = svgEl("g",{class:"ct-fb"}), ag = svgEl("g",{class:"ct-axis"}), og = svgEl("g",{class:"ct-overlay"});
  M.ndEl = new Map(); M.evEl = new Map(); M.og = og; M.svg = svg; M.scroller = scroller; M.yOf = yOf;
  const stat = stats(M); M.stat = stat;
  if (M.tree) svg.classList.add("tree");
  /*  */
  for (const n of M.uppers){
    const x = PAD + n.leaf_range[0]*CELL, w = Math.max(2, (n.leaf_range[1]-n.leaf_range[0])*CELL - 1);
    const c = M.tree ? M.cand.get(n.id) : null, st = M.tree ? stateOf(c, M.thr) : null;
    const g = svgEl("g",{class:`ct-nd up lv${Math.min(n.level,4)}${n.edge?.kind==="ContextFallback"?" fb":""}${st?" st-"+st:""}${M.tree && M.evNodes.has(n.id)?" ev":""}`, transform:`translate(${x},${yOf(n.level)})`, tabindex:0, role:"button", "data-node":n.id});
    g.append(svgEl("rect",{class:"bar", width:w, height:RH, rx:4}));
    const tag = `L${n.level}·${n.ordinal??"?"} · ${n.leaf_range[1]-n.leaf_range[0]} Leaf ${c?.rank!=null?` · #${c.rank}`:""}`;
    if (w >= 60){ g.append(svgEl("text",{class:"t1", x:5, y:11, text:tag}), svgEl("text",{class:"t2", x:5, y:25, text:fit(firstLine(M.T(n.text_hash)), w-10)})); }
    else if (w >= 22) g.append(svgEl("text",{class:"t1", x:3, y:11, text:c?.rank!=null?`#${c.rank}`:`L${n.level}`}));
    if (c?.jev && typeof c.jev.useful==="number"){ const bw = w-4; let cx = 2; for (const [k,cls] of [["useful","u"],["uncertain","q"],["not_useful","n"]]){ const ww = Math.max(0, Math.min(1, c.jev[k]??0))*bw; if (ww>0) g.append(svgEl("rect",{class:"tc "+cls, x:cx, y:RH-5, width:ww, height:3})); cx += ww; } }
    g.append(svgEl("title",{text:`L${n.level} Summary coglet ${shortId(n.id)} · Covered leaves ${n.leaf_range[0]}–${n.leaf_range[1]-1}(${n.leaf_range[1]-n.leaf_range[0]} leaves) · Actual inputs ${(n.inputs||[]).length}${n.edge?.kind==="ContextFallback"?" · Upward: ContextFallback (borrowed background)":n.edge?.kind?" · Upward "+n.edge.kind:" · Root"}${st?` · ${STATE_ZH[st]}${c?.jev?` · useful ${fmt(c.jev.useful,2)} / uncertain ${fmt(c.jev.uncertain,2)}`:""}${c?.rank!=null?` · #${c.rank}`:""}`:""}${M.tree && M.evNodes.has(n.id)?" · Covers evidence events":""}`}));
    ug.append(g); M.ndEl.set(n.id, g);
  }
  /* Leaf */
  for (const n of M.leaves){
    const x = PAD + n.leaf_range[0]*CELL, c = M.cand.get(n.id), st = stateOf(c, M.thr);
    const g = svgEl("g",{class:`ct-nd leaf st-${st}${M.evLeaves.has(n.id)?" ev":""}${n.edge?.kind==="ContextFallback"?" fb":""}`, transform:`translate(${x},${yOf(0)})`, tabindex:0, role:"button", "data-node":n.id});
    g.append(svgEl("rect",{class:"bar", width:CELL-1, height:RH, rx:2}));
    if (c?.jev && typeof c.jev.useful==="number"){ const bw = CELL-3; let cx = 1; for (const [k,cls] of [["useful","u"],["uncertain","q"],["not_useful","n"]]){ const ww = Math.max(0, Math.min(1, c.jev[k]??0))*bw; if (ww>0) g.append(svgEl("rect",{class:"tc "+cls, x:cx, y:RH-5, width:ww, height:3})); cx += ww; } }
    if (c?.rank!=null) g.append(svgEl("text",{class:"rk", x:(CELL-1)/2, y:13, "text-anchor":"middle", text:String(c.rank)}));
    const ev = n.origin ? M.evBySource.get(n.origin.source_id) : null;
    g.append(svgEl("title",{text:`L0 leaf coglet ${shortId(n.id)} · Leaf #${n.leaf_range[0]} · Event ${ev?`#${ev.event_sequence} ${ROLE_ZH[ev.role]||ev.role||"?"}`:"?"}${n.input_tokens!=null?` · L0 embedding input ${int(n.input_tokens)} tokens`:""}${c?` · ${STATE_ZH[st]}${c.jev?` · useful ${fmt(c.jev.useful,2)} / uncertain ${fmt(c.jev.uncertain,2)}`:c.p_use!=null?` · p_use ${fmt(c.p_use,2)}`:""}${c.rank!=null?` · #${c.rank}`:""}`:" · Not a candidate for this question"}${M.evLeaves.has(n.id)?" · Evidence event":""}`}));
    lg.append(g); M.ndEl.set(n.id, g);
  }
  /*  */
  for (const n of M.nodes.values()){
    if (n.edge?.kind!=="ContextFallback" || !n.edge.parent || !Array.isArray(n.leaf_range)) continue;
    const P = M.nodes.get(n.edge.parent); if (!P || !Array.isArray(P.leaf_range)) continue;
    const x1 = PAD + (n.leaf_range[0]+n.leaf_range[1])/2*CELL, y1 = yOf(n.level??0);
    const px0 = PAD + P.leaf_range[0]*CELL, px1 = PAD + P.leaf_range[1]*CELL - 1, x2 = Math.min(Math.max(x1, px0+6), px1-6), y2 = yOf(P.level) + RH;
    const d = `M${x1.toFixed(1)},${y1.toFixed(1)} C${x1.toFixed(1)},${(y1-GAP*2).toFixed(1)} ${x2.toFixed(1)},${(y2+GAP*2).toFixed(1)} ${x2.toFixed(1)},${y2.toFixed(1)}`;
    fg.append(svgEl("path",{class:"fb-edge", d, "marker-end":"url(#ct-m-fb)"}), svgEl("title",{text:`ContextFallback: ${shortId(n.id)} Borrows ${shortId(P.id)} as background, not summary coverage`}));
  }
  /*  */
  const ey = yOf(0) + RH + GAP;
  for (const e of M.events){
    if (!Array.isArray(e.leaf_range)) continue;
    const x = PAD + e.leaf_range[0]*CELL, w = Math.max(1, (e.leaf_range[1]-e.leaf_range[0])*CELL - 1);
    const isEv = M.evidence.has(e.source_id);
    const g = svgEl("g",{class:`ct-ev role-${e.role||"unknown"}${isEv?" evidence":""}`, transform:`translate(${x},${ey})`, tabindex:0, role:"button", "data-event":e.source_id});
    g.append(svgEl("rect",{class:"evbar", width:w, height:EVH-4, rx:3}));
    if (w >= 26) g.append(svgEl("text",{class:"evt", x:4, y:12, text:fit(`#${e.event_sequence??"?"} ${ROLE_ZH[e.role]||e.role||"?"}${isEv?" Evidence":""}`, w-6)}));
    g.append(svgEl("title",{text:`Event #${e.event_sequence??"?"} · ${ROLE_ZH[e.role]||e.role||"?"} · ${e.leaf_range[1]-e.leaf_range[0]} leaves (#${e.leaf_range[0]}–${e.leaf_range[1]-1})· source ${shortId(e.source_id)}${isEv?" · Dataset evidence event (post-hoc)":""}`}));
    eg.append(g); M.evEl.set(e.source_id, g);
  }
  /*  */
  const ay = ey + EVH + 2;
  for (let i = 0; i < M.N; i += 10){ const x = PAD + i*CELL; ag.append(svgEl("line",{x1:x, y1:ay, x2:x, y2:ay+3}), svgEl("text",{x:x+2, y:ay+12, text:String(i)})); }
  svg.append(eg, ug, lg, fg, ag, og);
  svg.addEventListener("click", ev => { const nd = ev.target.closest?.("[data-node]"); if (nd){ select(M, nd.getAttribute("data-node")); opts.onSelect?.(nd.getAttribute("data-node"), M); return; } const e = ev.target.closest?.("[data-event]"); if (e) opts.onEvent?.(e.getAttribute("data-event"), M); });
  svg.addEventListener("keydown", ev => { if (ev.key!=="Enter" && ev.key!==" ") return; const nd = ev.target.closest?.("[data-node]"); if (nd){ ev.preventDefault(); select(M, nd.getAttribute("data-node")); opts.onSelect?.(nd.getAttribute("data-node"), M); } });
  inner.append(lbls, svg); scroller.append(inner); container.replaceChildren(scroller);
  const api = {svg, scroller, select:id=>select(M,id), highlight:set=>highlight(M,set), scrollToLeaf:(i, smooth=true)=>{ const x = PAD + LBL + i*CELL - (scroller.clientWidth||800)/2; if (typeof scroller.scrollTo==="function") scroller.scrollTo({left:Math.max(0,x), behavior: smooth?"smooth":"auto"}); else scroller.scrollLeft = Math.max(0,x); }, cell:CELL, pad:PAD};
  M.ic = api; return api;
}
function pathIds(M, id){ const c = M.cand.get(id); if (c?.item?.path?.length) return c.item.path.map(p=>p.node); const out = [id]; const seen = new Set(out); let cur = M.nodes.get(id); while (cur?.edge?.parent && !seen.has(cur.edge.parent)){ out.push(cur.edge.parent); seen.add(cur.edge.parent); cur = M.nodes.get(cur.edge.parent); } return out; }
function select(M, id){
  for (const g of M.ndEl.values()){ g.classList.remove("sel"); g.classList.remove("on-path"); }
  M.og?.replaceChildren(); M.sel = id;
  if (!id) return;
  if (M.tree){ /*  */
    const n = M.nodes.get(id); M.ndEl.get(id)?.classList.add("sel");
    if (n && (n.level??0) > 0 && Array.isArray(n.leaf_range)) for (const lf of M.leaves) if (lf.leaf_range[0] >= n.leaf_range[0] && lf.leaf_range[0] < n.leaf_range[1]) M.ndEl.get(lf.id)?.classList.add("on-path");
    return;
  }
  const ids = pathIds(M, id);
  ids.forEach((nid, i) => { const g = M.ndEl.get(nid); if (!g) return; g.classList.add(i===0 ? "sel" : "on-path"); });
  /*  */
  const leaf = M.nodes.get(id); if (leaf && Array.isArray(leaf.leaf_range) && M.og){ const x = PAD + (leaf.leaf_range[0]+0.5)*CELL; const top = M.nodes.get(ids[ids.length-1]); const y2 = top ? M.yOf(top.level??0) : M.yOf(0); M.og.append(svgEl("line",{class:"path-line", x1:x, y1:M.yOf(0)+RH, x2:x, y2:y2})); }
}
function highlight(M, set){
  M.hl = set || null; M.svg?.classList.toggle("dim", !!set);
  for (const [id, g] of M.ndEl) g.classList.toggle("keep", !!set && set.has(id));
}

/*  */
function minimap(container, M, ic){
  ensureStyles();
  const draw = () => {
    const W = Math.max(200, container.clientWidth||800), H = 30, cw = W / M.N;
    const svg = svgEl("svg",{viewBox:`0 0 ${W} ${H}`, width:W, height:H, class:"ct-mini", role:"img", "aria-label":"Whole-scope minimap"});
    for (const n of M.leaves){ const st = stateOf(M.cand.get(n.id), M.thr); svg.append(svgEl("rect",{class:"st-"+st, x:(n.leaf_range[0]*cw).toFixed(2), y:4, width:Math.max(1, cw-0.3).toFixed(2), height:14})); }
    if (M.tree) for (const n of M.uppers){ const st = stateOf(M.cand.get(n.id), M.thr); if (st==="none") continue; svg.append(svgEl("rect",{class:"up st-"+st, x:(n.leaf_range[0]*cw).toFixed(2), y:0, width:Math.max(1.5, (n.leaf_range[1]-n.leaf_range[0])*cw-0.3).toFixed(2), height:3})); }
    for (const e of M.events){ if (!Array.isArray(e.leaf_range) || !M.evidence.has(e.source_id)) continue; svg.append(svgEl("rect",{class:"ev", x:(e.leaf_range[0]*cw).toFixed(2), y:21, width:Math.max(1.5, (e.leaf_range[1]-e.leaf_range[0])*cw).toFixed(2), height:4})); }
    const vp = svgEl("rect",{class:"vp", x:0, y:1, width:10, height:H-2, rx:2}); svg.append(vp);
    const sync = () => { const sc = ic.scroller; const total = sc.scrollWidth || 1; vp.setAttribute("x", (sc.scrollLeft/total*W).toFixed(1)); vp.setAttribute("width", Math.max(6, sc.clientWidth/total*W).toFixed(1)); };
    ic.scroller.addEventListener("scroll", sync, {passive:true}); (window.requestAnimationFrame||setTimeout)(sync);
    svg.addEventListener("click", ev => { const r = svg.getBoundingClientRect(); const i = Math.floor((ev.clientX - r.left)/r.width*M.N); ic.scrollToLeaf(i); });
    container.replaceChildren(svg, el("p",{class:"ct-sub",text:`Whole scope: ${M.N} Leaves · ${M.events.length} events · Colors show this question's state ${M.tree?"(top strip = ranges covered by retrieved / selected upper nodes)":""}; green marks indicate evidence events. Select a location to navigate.`}));
  };
  draw(); return {redraw:draw};
}

/*  */
function layersFor(M, leafId){
  const c = M.cand.get(leafId);
  if (c?.item?.memory?.length) return {source:"rendered", layers:c.item.memory.map(l => ({level:l.level, current:l.current, relation:l.relation, node:l.node, body:l.body, ordinal:l.ordinal, info:(l.info_hashes||[]).map(h=>M.T(h)), origins:l.origins||null}))};
  const leaf = M.nodes.get(leafId); if (!leaf) return null;
  const layers = [{level:0, current:0, relation:null, node:leaf.id, ordinal:leaf.ordinal, info:[M.T(leaf.text_hash)], origins:leaf.origin ? [leaf.origin] : null}];
  const seen = new Set([leaf.id]); let cur = leaf, fallback = false;
  while (cur?.edge?.parent && !seen.has(cur.edge.parent)){
    const P = M.nodes.get(cur.edge.parent); if (!P) break; seen.add(P.id);
    const idx = (P.inputs||[]).indexOf(cur.id), fb = cur.edge.kind==="ContextFallback" || idx < 0 || fallback; if (fb) fallback = true;
    let info = (P.input_summaries||[]).map(sm => M.T(sm?.output_hash)); if (!info.length || info.every(v=>v==null)){ const t = M.T(P.text_hash); info = t!=null ? t.split("\n\n") : [null]; }
    layers.push({level:P.level, current: fb ? null : idx, relation: fb ? "context_fallback" : (cur.edge.kind||"SummaryParent"), node:P.id, ordinal:P.ordinal, info, origins:null});
    cur = P;
  }
  return {source:"reconstructed", layers};
}
function layerPanel(container, M, leafId, opts={}){
  ensureStyles();
  const n = M.nodes.get(leafId), c = M.cand.get(leafId), j = M.jevT.get(leafId), st = stateOf(c, M.thr), L = layersFor(M, leafId);
  const box = el("div",{class:"ct-panel"});
  if (!n && !c){ box.append(el("p",{class:"ct-empty",text:`node ${shortId(leafId)} is absent from the snapshot visible to this question.`})); container.replaceChildren(box); return; }
  const ev = n?.origin ? M.evBySource.get(n.origin.source_id) : null;
  box.append(el("div",{class:"ct-ttl",text:`coglets ${shortId(leafId)} · Leaf #${n?.leaf_range?.[0]??"?"} · Event ${ev?`#${ev.event_sequence} ${ROLE_ZH[ev.role]||ev.role||"?"}`:"?"}${M.evLeaves.has(leafId)?" · Evidence event":""}`}));
  const chips = el("div",{class:"ct-chips"});
  chips.append(el("span",{class:"ct-badge st-"+st, text:STATE_ZH[st]+(c?.rank!=null?` #${c.rank}`:"")}));
  if (c?.outcome && c.outcome!=="selected") chips.append(el("span",{class:"ct-badge dim", text:OUTCOME_ZH[c.outcome]||c.outcome}));
  if (n?.input_tokens!=null) chips.append(el("span",{class:"ct-badge dim", title:"Embedding-input token count for this L0 vector point. Level-array embedding approximates whole-coglet size; leaf-body embedding uses only the leaf. Packing budgets use rendered coglet tokens; see step 3.", text:`L0 embedding input ${int(n.input_tokens)} tokens · memory_tokens budget ${int(M.s?.settings?.memory_tokens)}`}));
  if (c) chips.append(el("span",{class:"ct-badge dim", text:`Similarity ${fmt(c.similarity,3)}${c.vector_rank!=null?` · Vector #${c.vector_rank}`:""}`}));
  if (L) chips.append(el("span",{class:"ct-badge dim", text:L.source==="rendered" ? "Level array = model-facing search rendering" : "Level array reconstructed from snapshot, not search-time rendering"}));
  box.append(chips);
  if (c?.jev) box.append(el("div",{class:"ct-row"}, threeClassBar(c.jev), j?.provider ? el("span",{class:"ct-sub",text:` Provider choice ${j.provider.choice??"—"} · confidence ${fmt(j.provider.confidence,2)}${j.batch!=null?` · Batch ${j.batch+1}`:""}${j.model?` · ${j.model}`:""}`}) : null));
  else if (c) box.append(el("p",{class:"ct-sub",text:"JEV probabilities are unavailable for this coglet (not submitted, batch failure, or whole-pool fallback)."}));
  if (!L){ box.append(el("p",{class:"ct-empty",text:"No level array available."})); container.replaceChildren(box); return; }
  const stack = el("div",{class:"ct-stack"});
  const layers = L.layers.slice().sort((a,b)=>(b.level??0)-(a.level??0));
  layers.forEach((ly, i) => {
    const fb = ly.relation==="context_fallback" || (ly.current==null && (ly.level??0) > 0);
    const lb = el("div",{class:"ct-layer"+(fb?" fb":"")+(i===layers.length-1?" l0":"")});
    const head = el("div",{class:"ct-lh"}, el("b",{text:`L${ly.level??"?"}`}), el("span",{text:(ly.level??0)===0 ? "Original text" : fb ? "Borrowed prior context (context_fallback, current = null)" : `Summary level · current = ${ly.current}`}), ly.node ? el("code",{text:shortId(ly.node)}) : null, ly.ordinal!=null ? el("span",{class:"ct-sub",text:`Ordinal ${ly.ordinal}`}) : null);
    if (opts.onNode && ly.node && M.nodes.has(ly.node)) { head.style.cursor = "pointer"; head.title = "Locate this node in the tree"; head.addEventListener("click", () => opts.onNode(ly.node)); }
    lb.append(head);
    const ol = el("ol",{class:"ct-info"});
    (ly.info||[]).forEach((t, k) => { const isCur = ly.current===k; const li = el("li",{class:isCur?"cur":null}); const txt = t==null ? el("span",{class:"ct-empty",text:"(text not exported)"}) : (t.length > 320 ? el("details",{}, el("summary",{text:t.slice(0,320).replace(/\s+/g," ")+"…"}), el("div",{class:"ct-tx",text:t})) : el("div",{class:"ct-tx",text:t})); li.append(el("span",{class:"ct-k",text:`info[${k}]${isCur?" ← current":""}`}), txt); ol.append(li); });
    lb.append(ol);
    if ((ly.level??0)===0 && ly.origins?.length){ const o = ly.origins[0]; lb.append(el("p",{class:"ct-sub",text:`origins: event #${o.event_sequence??"?"} · ${ROLE_ZH[o.role]||o.role||"Role unknown"}${o.occurred_at?` · ${new Date(o.occurred_at/1000).toISOString().slice(0,19).replace("T"," ")}`:""}${o.source_span?` · Original bytes ${o.source_span[0]}–${o.source_span[1]}`:""}`})); }
    stack.append(lb);
    if (i < layers.length-1) stack.append(el("div",{class:"ct-link"+(fb?" fb":""), text: fb ? "↓ Background level has no current link to the level below" : "↓ current identifies the info entry corresponding to the immediately lower level"}));
  });
  box.append(el("h4",{text:"Coglet level array (upper summaries above, L0 original text below; this column is the model input)"}), stack);
  container.replaceChildren(box);
}

/*  */
let styled = false;
function ensureStyles(){
  if (styled || typeof document==="undefined") return; styled = true;
  const css = `
.ct-scroll{position:relative;overflow:auto;max-height:70vh;background:var(--surface);border:1px solid var(--grid);border-radius:8px}
.ct-inner{position:relative;display:flex;align-items:flex-start;min-width:max-content}
.ct-lbls{position:sticky;left:0;z-index:2;flex:0 0 ${LBL}px;width:${LBL}px;background:var(--surface);border-right:1px solid var(--grid)}
.ct-lbl{position:absolute;left:0;width:${LBL}px;display:flex;align-items:center;justify-content:center;font:600 10px var(--font);color:var(--muted)} .ct-lbl.ev{font-weight:500}
.ct-svg{display:block;font:9.5px var(--font);fill:var(--ink)}
.ct-svg .ph-bg{fill:var(--accent-bg)} .ct-svg .ph-fg{fill:var(--accent)} .ct-svg .m-fb{fill:var(--warm)}
.ct-nd{cursor:pointer} .ct-nd:focus{outline:none} .ct-nd:focus>rect.bar{stroke:var(--accent);stroke-width:2}
.ct-nd.up>rect.bar{fill:var(--surface-2);stroke:var(--axis);stroke-width:1}
.ct-nd.up.lv1>rect.bar{fill:var(--surface-2)} .ct-nd.up.lv2>rect.bar{fill:var(--neutral-bg)} .ct-nd.up.lv3>rect.bar,.ct-nd.up.lv4>rect.bar{fill:var(--surface)}
.ct-nd.up.fb>rect.bar{stroke:var(--warm);stroke-dasharray:4 3}
.ct-nd .t1{font-size:8.5px;fill:var(--muted);font-family:var(--mono)} .ct-nd .t2{font-size:9.5px;fill:var(--ink)}
.ct-nd.leaf>rect.bar{fill:var(--surface);stroke:var(--grid);stroke-width:1}
.ct-nd.leaf.st-R>rect.bar{fill:var(--surface-2);stroke:var(--axis)}
.ct-nd.leaf.st-low>rect.bar{fill:var(--surface-2);stroke:var(--axis)}
.ct-nd.leaf.st-cap>rect.bar{fill:var(--warm-bg);stroke:var(--warm)}
.ct-nd.leaf.st-pass>rect.bar{fill:url(#ct-hatch);stroke:var(--accent);stroke-width:1.2}
.ct-nd.leaf.st-S>rect.bar{fill:var(--accent);stroke:var(--accent-ink);stroke-width:1.4}
.ct-nd.leaf.ev>rect.bar{stroke:var(--good);stroke-width:1.6}
.ct-nd.leaf.fb>rect.bar{stroke-dasharray:3 2}
.ct-nd .rk{font-size:8.5px;font-weight:700;fill:#fff;font-family:var(--mono)}
.ct-nd .tc.u{fill:var(--accent-ink)} .ct-nd .tc.q{fill:var(--warn)} .ct-nd .tc.n{fill:var(--axis)}
.ct-nd.leaf.st-S .tc.u{fill:#fff}
.ct-nd.sel>rect.bar{stroke:var(--ink);stroke-width:2.4} .ct-nd.on-path>rect.bar{stroke:var(--warn-text);stroke-width:2.2}
.ct-svg.tree .ct-nd.up.st-R>rect.bar,.ct-svg.tree .ct-nd.up.st-low>rect.bar{fill:var(--surface-2);stroke:var(--axis);stroke-width:1.2}
.ct-svg.tree .ct-nd.up.st-cap>rect.bar{fill:var(--warm-bg);stroke:var(--warm)}
.ct-svg.tree .ct-nd.up.st-pass>rect.bar{fill:url(#ct-hatch);stroke:var(--accent);stroke-width:1.2}
.ct-svg.tree .ct-nd.up.st-S>rect.bar{fill:var(--accent);stroke:var(--accent-ink);stroke-width:1.4} .ct-svg.tree .ct-nd.up.st-S .t1,.ct-svg.tree .ct-nd.up.st-S .t2{fill:#fff} .ct-svg.tree .ct-nd.up.st-S .tc.u{fill:#fff}
.ct-svg.tree .ct-nd.up.ev>rect.bar{stroke:var(--good);stroke-width:1.6} .ct-svg.tree .ct-nd.up.st-S.ev>rect.bar{stroke:var(--good)}
.ct-svg.tree.build-only .ct-nd.up>rect.bar{fill:var(--surface-2)!important;stroke:var(--axis)!important;stroke-width:1!important} .ct-svg.tree.build-only .ct-nd.up.ev>rect.bar{stroke:var(--good)!important}
.ct-mini rect.up.st-S{fill:var(--accent)} .ct-mini rect.up.st-pass{fill:var(--accent-bg)} .ct-mini rect.up.st-low,.ct-mini rect.up.st-R{fill:var(--axis)} .ct-mini rect.up.st-cap{fill:var(--warm)}
.ct-fb .fb-edge{fill:none;stroke:var(--warm);stroke-width:1.2;stroke-dasharray:4 3}
.ct-ev{cursor:pointer} .ct-ev>rect.evbar{fill:var(--surface-2);stroke:var(--grid)} .ct-ev.role-user>rect.evbar{fill:var(--neutral-bg)} .ct-ev.role-assistant>rect.evbar{fill:var(--surface-2)}
.ct-ev.evidence>rect.evbar{fill:var(--good-bg);stroke:var(--good);stroke-width:1.4} .ct-ev .evt{font-size:8.5px;fill:var(--ink-2)} .ct-ev.evidence .evt{fill:var(--good-text);font-weight:600}
.ct-axis line{stroke:var(--axis)} .ct-axis text{font-size:8.5px;fill:var(--muted);font-family:var(--mono)}
.ct-overlay .path-line{stroke:var(--warn);stroke-width:5;stroke-opacity:.35;stroke-linecap:round}
.ct-svg.dim .ct-nd:not(.keep){opacity:.2}
.ct-svg.build-only .ct-nd.leaf>rect.bar{fill:var(--surface)!important;stroke:var(--grid)!important;stroke-width:1!important} .ct-svg.build-only .ct-nd.leaf.ev>rect.bar{stroke:var(--good)!important} .ct-svg.build-only .ct-nd .tc,.ct-svg.build-only .ct-nd .rk,.ct-svg.build-only .ct-overlay{display:none}
.ct-mini{display:block;width:100%;height:30px;border:1px solid var(--grid);border-radius:6px;background:var(--surface);cursor:pointer}
.ct-mini rect{fill:var(--surface-2)} .ct-mini rect.st-low,.ct-mini rect.st-R{fill:var(--axis)} .ct-mini rect.st-cap{fill:var(--warm)} .ct-mini rect.st-pass{fill:var(--accent-bg);stroke:var(--accent);stroke-width:.4} .ct-mini rect.st-S{fill:var(--accent)} .ct-mini rect.ev{fill:var(--good)}
.ct-mini rect.vp{fill:none;stroke:var(--ink);stroke-width:1.2;stroke-opacity:.7}
.ct-sub{font-size:12px;color:var(--muted);margin:4px 0}
.ct-tcb{display:inline-flex;align-items:center;gap:8px;flex-wrap:wrap} .ct-tcb .bar{display:inline-flex;width:160px;height:10px;border-radius:5px;overflow:hidden;background:var(--neutral-bg);border:1px solid var(--grid)} .ct-tcb.compact .bar{width:90px;height:7px}
.ct-tcb .bar i{display:block;height:100%} .ct-tcb .bar i.u{background:var(--accent)} .ct-tcb .bar i.q{background:var(--warn)} .ct-tcb .bar i.n{background:var(--axis)} .ct-tcb .lbl{font:12px var(--mono);color:var(--ink-2)} .ct-tcb-na{font-size:12px;color:var(--muted)}
.ct-panel{min-width:0} .ct-ttl{font-weight:600;font-size:13.5px;margin-bottom:6px} .ct-chips{display:flex;flex-wrap:wrap;gap:4px 6px;margin:4px 0} .ct-row{display:flex;flex-wrap:wrap;gap:6px 10px;align-items:center;margin:6px 0}
.ct-badge{display:inline-flex;align-items:center;gap:4px;font-size:12px;padding:1px 8px;border-radius:999px;white-space:nowrap;border:1px solid var(--grid);background:var(--surface-2);color:var(--ink-2)}
.ct-badge.st-S{background:var(--accent-bg);color:var(--accent-ink);border-color:var(--accent)} .ct-badge.st-pass{background:var(--accent-bg);color:var(--accent-ink)} .ct-badge.st-cap{background:var(--warm-bg);color:var(--warm-text)} .ct-badge.st-none{color:var(--muted)}
.ct-panel h4{font-size:13px;margin:12px 0 6px;color:var(--ink-2)}
.ct-stack{display:flex;flex-direction:column;gap:2px}
.ct-layer{background:var(--surface-2);border:1px solid var(--grid);border-left:3px solid var(--accent);border-radius:8px;padding:8px 10px;min-width:0}
.ct-layer.fb{border-left-color:var(--warm);border-style:dashed;border-left-style:solid} .ct-layer.l0{border-left-color:var(--axis);background:var(--surface)}
.ct-lh{display:flex;flex-wrap:wrap;gap:4px 8px;align-items:center;font-size:12.5px;color:var(--ink-2);margin-bottom:4px} .ct-lh b{color:var(--ink)} .ct-lh code{font:11.5px var(--mono);color:var(--muted)}
.ct-info{margin:0;padding-left:0;list-style:none;display:flex;flex-direction:column;gap:4px} .ct-info li{border:1px solid var(--grid);border-radius:6px;padding:4px 8px;background:var(--surface);min-width:0}
.ct-info li.cur{border-color:var(--accent);box-shadow:0 0 0 2px var(--accent-bg)} .ct-k{display:block;font:11px var(--mono);color:var(--muted);margin-bottom:2px} .ct-info li.cur .ct-k{color:var(--accent-ink);font-weight:700}
.ct-tx{white-space:pre-wrap;word-break:break-word;font-size:12px;max-height:200px;overflow:auto} .ct-info details>summary{cursor:pointer;font-size:12px;color:var(--accent-ink)}
.ct-link{font-size:11.5px;color:var(--accent-ink);padding-left:10px} .ct-link.fb{color:var(--warm-text)}
.ct-empty{color:var(--muted);font-style:italic;font-size:12.5px}
.ct-legend{display:flex;flex-wrap:wrap;gap:4px 14px;font-size:12px;color:var(--ink-2);margin:8px 0 4px}
.ct-legend i{display:inline-block;width:12px;height:12px;vertical-align:-1px;margin-right:5px;border-radius:3px;border:1px solid var(--axis);background:var(--surface)}
.ct-legend i.S{background:var(--accent);border-color:var(--accent-ink)} .ct-legend i.pass{background:var(--accent-bg);border-color:var(--accent);background-image:repeating-linear-gradient(45deg,transparent 0 3px,var(--accent) 3px 4px)} .ct-legend i.low{background:var(--surface-2)} .ct-legend i.none{background:var(--surface)} .ct-legend i.ev{border-color:var(--good);border-width:2px} .ct-legend i.up{background:var(--surface-2);width:22px} .ct-legend i.fb{border:1px dashed var(--warm);width:22px}
.ct-legend i.tc{width:22px;height:6px;border:0;border-radius:2px;background:linear-gradient(90deg,var(--accent) 0 60%,var(--warn) 60% 75%,var(--axis) 75% 100%)}`;
  const st = document.createElement("style"); st.id = "coglet-tree-css"; st.textContent = css; document.head.append(st);
}
function legend(opts={}){
  ensureStyles();
  if (opts.tree) return el("div",{class:"ct-legend"},
    el("span",{}, el("i",{class:"S"}), "Selected S (any level; rank inside upper bars; packed into context)"), el("span",{}, el("i",{class:"pass"}), "Passed JEV but exceeded packing budget"), el("span",{}, el("i",{class:"low"}), "Candidate, but JEV rejected or unscored"), el("span",{}, el("i",{class:"none"}), "Not a candidate"),
    el("span",{}, el("i",{class:"tc"}), "Bottom segments = useful / uncertain / not_useful"), el("span",{}, el("i",{class:"up"}), "Upper summary spans covered leaves; item = node\'s own body"), el("span",{}, el("i",{class:"fb"}), "ContextFallback: dashed border and orange arrow to borrowed background"), el("span",{}, el("i",{class:"ev"}), "Green border = evidence leaves and upper nodes covering evidence (coverage diagnostic; only L0 contributes direct sources)"));
  return el("div",{class:"ct-legend"},
    el("span",{}, el("i",{class:"S"}), "Selected S (rank inside; packed into context)"), el("span",{}, el("i",{class:"pass"}), "Passed JEV but exceeded budget (CONTEXT_BUNDLE_TOO_LARGE)"), el("span",{}, el("i",{class:"low"}), "Candidate, but JEV rejected or unscored"), el("span",{}, el("i",{class:"none"}), "Not a candidate"),
    el("span",{}, el("i",{class:"tc"}), "Bottom segments = useful / uncertain / not_useful"), el("span",{}, el("i",{class:"up"}), "Upper summary spans covered leaves"), el("span",{}, el("i",{class:"fb"}), "ContextFallback: dashed border and orange arrow to borrowed background"), el("span",{}, el("i",{class:"ev"}), "Green border = evidence-event leaves (post-hoc diagnostic)"));
}

window.CogletTree = {model, stats, icicle, minimap, layerPanel, layersFor, threeClassBar, legend, stateOf, pathIds, select, highlight, STATE_ZH, OUTCOME_ZH, ROLE_ZH, STATE_ORDER, el, svgEl, fmt, int, shortId, fit, firstLine};
})();
