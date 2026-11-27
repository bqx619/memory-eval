/* memeval report renderer — reads #memeval-data {reports:[report.json...], compare?:delta.json} */
(function(){
"use strict";
const data = JSON.parse(document.getElementById("memeval-data").textContent);
const reports = data.reports || [];
const compare = data.compare || null;
const SERIES = ["var(--s1)","var(--s2)","var(--s3)"];
const METRIC_ZH = {hit:"Hit rate Hit@K",recall:"Recall@K",all_evidence:"All evidence All-evidence@K",mrr:"MRR",ndcg:"nDCG@K",precision:"Precision (exhaustive labels only)",empty_result_correct:"Empty-result accuracy",pinned_coverage:"Pinned-memory coverage",exact_match_diagnostic:"Lexical EM (Diagnostic)",token_f1_diagnostic:"Lexical F1 (Diagnostic)",choice_accuracy:"Choice accuracy"};
const PHASE_ZH = {reset_ms:"reset",ingest_ms:"ingest",ready_ms:"ready",query_ms:"query",answer_ms:"answer"};
const passLabel = r => (r.mode==="fixed_reader"||r.mode==="native_answer") ? "Answer accuracy (official-equivalent)" : "Source-assertion pass rate";
const STATUS_ZH = {scored:"Scored",invalid:"Invalid answer",unscored:"Unscored",unsupported:"Unsupported",contract_error:"Contract error",error:"Error",ingest_failed:"Ingest failure"};

/* ---------- helpers ---------- */
const el = (tag, attrs, ...kids) => { const n = document.createElement(tag); if (attrs) for (const [k,v] of Object.entries(attrs)) { if (k==="class") n.className=v; else if (k==="text") n.textContent=v; else if (k.startsWith("on")) n.addEventListener(k.slice(2), v); else if (v!==null && v!==undefined) n.setAttribute(k, v); } for (const k of kids.flat()) if (k!==null && k!==undefined) n.append(k.nodeType? k : document.createTextNode(String(k))); return n; };
const svgEl = (tag, attrs, ...kids) => { const n = document.createElementNS("http://www.w3.org/2000/svg", tag); for (const [k,v] of Object.entries(attrs||{})) { if (k==="class") n.setAttribute("class", v); else if (k==="text") n.textContent=v; else n.setAttribute(k, v); } for (const k of kids.flat()) if (k) n.append(k); return n; };
const isNum = x => typeof x === "number" && Number.isFinite(x);
const pct = x => isNum(x) ? (x*100).toFixed(1) + "%" : "—";
const num = (x, d=2) => isNum(x) ? x.toFixed(d) : "—";
const int = x => isNum(x) ? x.toLocaleString("zh-CN") : "—";
const short = s => (s||"").slice(0,12);
const sutName = r => (r.sut && (r.sut.variant && r.sut.variant !== r.sut.name ? `${r.sut.name} · ${r.sut.variant}` : r.sut.name)) || "sut";
const statusCls = r => r.status==="scored" ? (r.passed===true ? "v-pass" : r.passed===false ? "v-fail" : "v-none") : (r.status==="unscored"||r.status==="unsupported" ? "v-none" : "v-warn");
const statusIcon = (kind, label) => el("span",{class:"st "+kind}, el("span",{class:"ic",text: kind==="good"?"✓":kind==="bad"?"✕":kind==="warn"?"!":"–"}), label);

/* ---------- tooltip ---------- */
function attachTooltip(container){
  const tip = el("div",{class:"tip",role:"status","aria-live":"polite"});
  container.append(tip);
  return {
    show(target, rows){ tip.replaceChildren(...rows.map(r => el("div",{class:"row"}, el("span",{}, r.key? el("span",{class:"key",style:`background:${r.key}`}) : null, r.label), el("span",{class:"v",text:r.value})))); tip.classList.add("show");
      const cb = container.getBoundingClientRect(), tb = target.getBoundingClientRect();
      let left = tb.right - cb.left + 8, top = tb.top - cb.top - 4; if (left + 220 > cb.width) left = Math.max(0, tb.left - cb.left - 228); tip.style.left = left+"px"; tip.style.top = top+"px"; },
    hide(){ tip.classList.remove("show"); }
  };
}

/* ---------- horizontal bar chart (1..3 series) ---------- */
function barChart({rows, series, caption, tableHead}){
  const multi = series.length > 1;
  const longest = Math.max(0, ...rows.map(r => String(r.label||"").length));
  const labelW = Math.max(150, Math.min(330, 24 + longest*6.6)), valW = multi ? 16 : 56, W = 900, padT = multi ? 22 : 6, bar = multi ? 12 : 18, gap = 2;
  const fit = s => { s = String(s||""); const max = Math.floor((labelW-14)/6.6); return s.length <= max ? s : s.slice(0, Math.ceil(max/2)-1) + "…" + s.slice(-(Math.floor(max/2)-1)); };
  const rowH = multi ? series.length*(bar+gap) + 12 : 30;
  const plotW = W - labelW - valW - 12, H = padT + rows.length*rowH + 14;
  const x = v => labelW + Math.max(0, Math.min(1, v||0)) * plotW;
  const svg = svgEl("svg",{viewBox:`0 0 ${W} ${H}`, role:"img","aria-label":caption});
  const clip = svgEl("clipPath",{id:"clip"+Math.random().toString(36).slice(2)}); clip.append(svgEl("rect",{x:labelW,y:0,width:plotW+valW,height:H})); svg.append(svgEl("defs",{},clip));
  if (multi) for (const t of [0,.25,.5,.75,1]) { svg.append(svgEl("line",{class:"grid",x1:x(t),x2:x(t),y1:padT-4,y2:H-14})); svg.append(svgEl("text",{class:"tick",x:x(t),y:12,"text-anchor":"middle",text:(t*100)+"%"})); }
  svg.append(svgEl("line",{class:"base",x1:labelW,x2:labelW,y1:padT-4,y2:H-14}));
  const wrap = el("div",{class:"chart"}); const tip = attachTooltip(wrap);
  rows.forEach((row,i) => {
    const y0 = padT + i*rowH;
    { const t = svgEl("text",{class:"cat",x:labelW-10,y:y0 + rowH/2 + 4,"text-anchor":"end",text:fit(row.label)}); if (fit(row.label)!==row.label) t.append(svgEl("title",{text:row.label})); svg.append(t); }
    row.values.forEach((cell,j) => {
      const by = multi ? y0 + 6 + j*(bar+gap) : y0 + (rowH-bar)/2;
      const w = x(cell.v) - labelW;
      const g = svgEl("g",{"clip-path":`url(#${clip.id})`});
      const hit = svgEl("rect",{class:"hit",x:labelW,y:by-3,width:plotW+valW,height:bar+6,tabindex:0,role:"img","aria-label":`${row.label} ${series[j].name} ${pct(cell.v)}`});
      const rect = svgEl("rect",{class:"bar",x:labelW-4,y:by,width:Math.max(0,w)+4,height:bar,rx:4,fill:series[j].color});
      const rowsTip = () => row.values.map((c,k)=>({key:series[k].color,label:series[k].name,value:`${pct(c.v)}  (${c.num ?? "—"}/${c.den ?? "—"})`}));
      hit.addEventListener("pointerenter",()=>tip.show(hit,rowsTip())); hit.addEventListener("focus",()=>tip.show(hit,rowsTip()));
      hit.addEventListener("pointerleave",()=>tip.hide()); hit.addEventListener("blur",()=>tip.hide());
      g.append(hit, rect); svg.append(g);
      if (!multi) svg.append(svgEl("text",{class:"val",x:x(cell.v)+6,y:by+bar-4,text:pct(cell.v)}));
    });
  });
  wrap.append(svg);
  // table twin
  const table = el("table",{class:"tbl",hidden:""}, el("thead",{}, el("tr",{}, el("th",{text:tableHead||"Category"}), ...series.map(s=>el("th",{class:"num",text:s.name})))),
    el("tbody",{}, rows.map(r => el("tr",{}, el("td",{text:r.label}), ...r.values.map(c => el("td",{class:"num",text:`${pct(c.v)} (${c.num ?? "—"}/${c.den ?? "—"})`}))))));
  const toggle = el("button",{"aria-pressed":"false",text:"Table view",onclick:()=>{ const on = toggle.getAttribute("aria-pressed")!=="true"; toggle.setAttribute("aria-pressed",on); table.hidden=!on; wrap.hidden=on; }});
  const legend = multi ? el("div",{class:"legend"}, series.map(s=>el("span",{}, el("span",{class:"sw",style:`background:${s.color}`}), s.name))) : null;
  const fig = el("figure",{}, legend, el("div",{class:"chartwrap"},wrap), el("div",{class:"scroll"},table), el("figcaption",{text:caption}));
  return {fig, toggle};
}

/* ---------- sections ---------- */
function header(){
  const r = reports[0];
  const iso = r.isolation||{}; const tierZh = {A:"Retired process sandbox",B:"Retired microVM",C:"In-process (no sandbox)",D:"Container (OCI, Linux runtime)"}[iso.tier] || iso.tier;
  const chips = el("div",{class:"chips"},
    el("span",{class:"chip"}, "Suite ", el("b",{text:r.suite_id})),
    el("span",{class:"chip"}, "Protocol ", el("b",{text:(r.protocol||{}).version||"—"})),
    el("span",{class:"chip"}, "Mode ", el("b",{text:r.mode})),
    reports.length>1 ? el("span",{class:"chip"}, "Isolation ", el("b",{text:"See the per-system cards below"})) : el("span",{class:"chip"}, "Isolation ", el("b",{text:`Tier ${iso.tier||"?"} · ${tierZh||""}`}), iso.tier==="C" ? " · In-process baseline, no network calls" : iso.network_mode ? ` · Network ${iso.network_mode}${iso.network_enforced?"":" (Not enforced)"}` : null),
    el("span",{class:"chip"}, "Evaluator LLM ", el("b",{text: r.evaluator_llm ? (r.evaluator_llm.mode||"Enabled") : "Not used"})),
    el("span",{class:"chip"}, "Repeats ", el("b",{text:String(r.repeats ?? 1)})),
    el("span",{class:"chip mono"}, "seed ", el("b",{text:short(r.run_seed)})),
  );
  const theme = el("button",{text:"Theme: auto",onclick:()=>{ const cur = document.documentElement.getAttribute("data-theme"); const next = !cur ? "light" : cur==="light" ? "dark" : null; if (next) document.documentElement.setAttribute("data-theme",next); else document.documentElement.removeAttribute("data-theme"); theme.textContent = "Theme: " + ({light:"Light",dark:"Dark"}[next]||"Auto"); try{ localStorage.setItem("memeval-theme", next||""); }catch(e){} }});
  return el("div",{class:"hdr"}, el("div",{}, el("div",{class:"muted",text:"MEMEVAL · Personal-agent memory evaluation"}), el("h1",{text: reports.length>1 ? `${reports.length} systems · Comparison report` : `${sutName(r)} · Evaluation report`}), el("p",{class:"sub",text:`${r.started_at_utc || ""} · ${(r.environment||{}).platform||""} · ${(r.environment||{}).go_version||""}`}), chips), el("div",{class:"tools"}, theme));
}

function notice(){
  return el("div",{class:"notice"}, "Scores apply only to the stated sources, protocol, budgets, and coverage. They are neither a universal agent score nor an official leaderboard. A null-memory system may pass exclusion assertions; also inspect positive-evidence recall and empty-result accuracy. Unknown costs and capabilities are shown as —, not zero.");
}

function officialInfo(r){
  const off = (r.scores||{}).official; if (!off) return null;
  const types = off.by_type_metrics||{}; const first = Object.values(types)[0];
  const primary = off.primary_metric || (first ? Object.keys(first)[0] : null);
  const prefix = primary ? primary.split(".")[0] : null;
  return {off, primary, prefix, types};
}

function kpis(r){
  const s = r.summary||{}, mo = (r.scores||{}).memory_only||{}; const sc = s.status_counts||{};
  const issues = (sc.error||0)+(sc.contract_error||0)+(sc.ingest_failed||0);
  const tile = (lbl, val, sub, hero) => el("div",{class:"tile"}, el("div",{class:"lbl",text:lbl}), el("div",{class:"val"+(hero?" hero":""),text:val}), sub? el("div",{class:"sub",text:sub}):null);
  const oi = officialInfo(r); const offScore = oi && isNum(oi.off.score) && !isNum(s.pass_rate_on_scored);
  const hasPass = isNum(s.pass_rate_on_scored) || offScore;
  const passTile = offScore
    ? tile(`Official primary metric · ${oi.off.benchmark||"—"}`, pct(oi.off.score), `${oi.primary||"score"} · Fidelity ${oi.off.fidelity||oi.off.fidelity||off.protocol_fidelity||"—"} · Deterministic scoring, no judge`, true)
    : tile(passLabel(r), pct(s.pass_rate_on_scored), isNum(s.pass_rate_on_scored) ? `Pass ${int(s.passed)} / Valid judgments ${int(s.n_with_pass_judgment)}` : "No pass judgment for this suite (retrieval metrics only)", isNum(s.pass_rate_on_scored));
  const recallTile = tile("Positive-evidence recall Recall@K", pct(mo.recall && mo.recall.mean), `Cases with positive evidence n=${mo.recall ? mo.recall.n : "—"}`, !hasPass);
  return el("div",{class:"kpis"},
    ...(hasPass ? [passTile, recallTile] : [recallTile, passTile]),
    tile("Empty-result accuracy", pct(mo.empty_result_correct && mo.empty_result_correct.mean), `Cases expecting empty results n=${mo.empty_result_correct ? mo.empty_result_correct.n : "—"}`),
    tile("Scoring coverage", pct(s.scoring_coverage), `Unscored ${int(sc.unscored||0)} · Unsupported ${int(s.unsupported||0)}`),
    tile("Execution issues", int(issues), `Error ${int(sc.error||0)} · Contract ${int(sc.contract_error||0)} · Ingest failure ${int(sc.ingest_failed||0)}`),
  );
}

function categoryCard(rs){
  const cats = Array.from(new Set(rs.flatMap(r => Object.keys(r.by_category||{})))).sort((a,b)=>a.localeCompare(b,"zh"));
  const series = rs.map((r,i)=>({name:sutName(r),color:SERIES[i]}));
  const anyPass = rs.some(r => Object.values(r.by_category||{}).some(s => isNum(s.pass_rate_on_scored)));
  const pick = s => anyPass ? {v:s.pass_rate_on_scored, num:s.passed, den:s.n_with_pass_judgment} : {v: s.metrics && s.metrics.recall ? s.metrics.recall.mean : null, num: s.metrics && s.metrics.recall ? s.metrics.recall.n : null, den: s.metrics && s.metrics.recall ? s.metrics.recall.n : null};
  const rows = cats.map(c => ({label:c, values: rs.map(r => pick((r.by_category||{})[c]||{}))}));
  const what = anyPass ? passLabel(rs[0]) : "Positive-evidence recall Recall@K (Mean)";
  const {fig,toggle} = barChart({rows, series, caption: rs.length>1 ? `By category: ${what}, grouped by SUT; parentheses show ${anyPass?"passed / valid judgments":"samples with values"}. ` : `By category: ${what}; parentheses show ${anyPass?"passed / valid judgments":"samples with values"}. Hover for details.`});
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:"Category results · "+what}), toggle), fig);
}

function scoresCard(r){
  const mo = (r.scores||{}).memory_only||{}; const off = (r.scores||{}).official; const oi = officialInfo(r);
  const isOfficialKey = k => (oi && oi.prefix && k.startsWith(oi.prefix+".")) || k.startsWith("mcq.");
  const metricRow = k => el("tr",{}, el("td",{text:METRIC_ZH[k]||k}), el("td",{class:"num",text:num(mo[k].mean,4)}), el("td",{class:"num",text:int(mo[k].n)}));
  const rows = Object.keys(mo).sort().filter(k => !isOfficialKey(k)).map(metricRow);
  const offRows = Object.keys(mo).sort().filter(isOfficialKey).map(metricRow);
  const typeKeys = oi ? Array.from(new Set(Object.values(oi.types).flatMap(v => Object.keys(v)))).sort() : [];
  const typeTable = oi && Object.keys(oi.types).length ? el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Question type / category"}), ...typeKeys.map(k => el("th",{class:"num",text:k})))),
    el("tbody",{}, Object.keys(oi.types).sort().map(t => el("tr",{}, el("td",{text:t}), ...typeKeys.map(k => el("td",{class:"num",text:num(oi.types[t][k],4)}))))))) : null;
  const devs = off && (off.deviations||[]).length ? el("details",{}, el("summary",{class:"muted",text:`Protocol deviations: ${off.deviations.length} items`}), el("ul",{style:"margin:4px 0 0;padding-left:16px"}, off.deviations.map(x => el("li",{class:"muted",text:x})))) : null;
  const official = off ? el("div",{}, el("dl",{class:"kv"}, el("dt",{text:"Benchmark"}), el("dd",{text:off.benchmark||"—"}), el("dt",{text:"Judge"}), el("dd",{text:off.judge ? (typeof off.judge==="string" ? off.judge : `${off.judge.provider||""}/${off.judge.model||""}${off.judge.kind === "decision" ? ` · P(true) ≥ ${off.judge.cutoff ?? 0.5} · argmax` : ""}`) : "— (Deterministic scoring)"}), el("dt",{text:"Protocol fidelity"}), el("dd",{text:off.fidelity||off.protocol_fidelity||"—"}), el("dt",{text:"Primary metric"}), el("dd",{}, el("code",{text:oi && oi.primary ? oi.primary : "score"})), el("dt",{text:"Score"}), el("dd",{text:pct(off.score)})),
      offRows.length ? el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Official metric details"}), el("th",{class:"num",text:"Mean"}), el("th",{class:"num",text:"Samples with values n"}))), el("tbody",{}, offRows))) : null,
      typeTable, devs)
    : el("p",{class:"muted",text:"No judge/fixed reader was run, so there is no official protocol score. Full fidelity requires matching the official judge model, templates, scorer revision, cases, and denominator."});
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:"Two score tracks"})), el("div",{class:"grid2"},
    el("div",{}, el("h3",{text:"Official protocol score official_protocol_score"}), official),
    el("div",{}, el("h3",{text:"Memory retrieval score memory_only_retrieval_score"}), el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Metric"}), el("th",{class:"num",text:"Mean"}), el("th",{class:"num",text:"Samples with values n"}))), el("tbody",{}, rows))))));
}

function timingsCard(r){
  const t = (r.summary||{}).timings||{};
  const rows = Object.keys(PHASE_ZH).filter(k=>t[k]).map(k => el("tr",{}, el("td",{text:PHASE_ZH[k]}), el("td",{class:"num",text:num(t[k].p50,2)}), el("td",{class:"num",text:num(t[k].p95,2)}), el("td",{class:"num",text:num(t[k].total,0)}), el("td",{class:"num",text:int(t[k].n)})));
  const b = (r.summary||{}).billing||{}; const rc = (r.summary||{}).returned_chars||{};
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:"Latency and cost"}), el("small",{text:"Milliseconds measured by the evaluator; not a cloud-performance guarantee"})),
    el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Stage"}), el("th",{class:"num",text:"p50"}), el("th",{class:"num",text:"p95"}), el("th",{class:"num",text:"Total"}), el("th",{class:"num",text:"n"}))), el("tbody",{}, rows))),
    el("p",{class:"ink2"}, `Returned characters p50 ${int(rc.p50)} · p95 ${int(rc.p95)} (Unicode code points, not tokens). `),
    el("p",{class:"ink2"}, "Cost: SUT ", el("b",{text: (r.billing && r.billing.sut) ? "Self-reported calls " + int(r.billing.sut.self_reported_calls) : "Unmetered (—)"}), " · Evaluator ", el("b",{text: (r.billing && r.billing.evaluator) ? `${int(r.billing.evaluator.tokens)} tokens / $${num(r.billing.evaluator.cost_usd,4)}` : "No LLM used (—)"})));
}

function benchmarkCell(b){
  if (!b || !b.protocol_id) return el("span",{class:"null",text:"—  (No benchmark protocol declaration)"});
  const src = b.source==="suite" ? "From suite" : b.source==="registry" ? "From registry" : (b.source||"—");
  return el("span",{}, el("code",{text:`${b.protocol_id}#${b.variant||"—"}`}), ` v${b.version||"—"} · Declaration SHA-256 `, el("code",{text:short(b.sha256)}), ` · ${src}`);
}

function dataFlowPanel(df, enforced){
  const roleZh = {sut_llm_gateway:"SUT → LLM gateway", sut_provider:"SUT → provider (direct)", evaluator_llm:"Evaluator → judge/reader models", sidecar_image_registry:"Build → image registry"};
  const summaryZh = {zero_egress:"Zero egress", sut_gateway:"LLM gateway only", sut_provider:"SUT direct provider", evaluator_providers:"Evaluator provider calls"};
  const summary = String(df.summary||"—").split("+").map(s => summaryZh[s]||s).join(" + ");
  const rows = (df.parties||[]).map(p => el("tr",{}, el("td",{text: roleZh[p.role]||p.role||"—"}), el("td",{}, el("code",{text: p.endpoint||"—"})), el("td",{text: p.direction||"—"}), el("td",{}, p.enforcement==="relay_single_target" ? statusIcon("good","Single-target relay") : p.enforcement==="none" ? statusIcon("bad","None") : statusIcon("na", p.enforcement||"—")), el("td",{class:"muted",text: Array.isArray(p.evidence) ? (p.evidence.join("; ")||"—") : (p.evidence||"—")})));
  const proof = df.zero_egress_proof;
  return el("div",{}, el("h3",{text:"Data flow"}),
    el("p",{}, el("b",{text:"Summary: "}), summary, proof ? el("span",{}, " · Zero-egress evidence ", enforced(typeof proof==="object" ? !!proof.verified : !!proof), proof && typeof proof==="object" && proof.selftest_sha256 ? el("code",{text:" "+short(proof.selftest_sha256)}) : null) : null),
    rows.length ? el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Party"}), el("th",{text:"Endpoint"}), el("th",{text:"Direction"}), el("th",{text:"Enforcement"}), el("th",{text:"Evidence"}))), el("tbody",{}, rows))) : el("p",{class:"muted",text:"No egress parties were recorded."}),
    (df.notes||[]).length ? el("ul",{class:"muted"}, (df.notes||[]).map(n => el("li",{text:n}))) : null);
}
function sutBillingPanel(b, kv){
  const n = v => v==null ? "—" : int(v);
  return el("div",{}, el("h3",{text:"SUT model usage through the gateway"}), kv([
    ["Calls", `${n(b.calls)}${b.retries ? ` (including retries ${n(b.retries)})` : ""}${b.unmetered_calls ? ` · Unmetered ${n(b.unmetered_calls)}` : ""}`],
    ["prompt tokens", `${n(b.prompt_tokens)}${b.cached_tokens ? ` (cached ${n(b.cached_tokens)})` : ""}`],
    ["completion tokens", `${n(b.completion_tokens)}${b.reasoning_tokens ? ` (Reasoning ${n(b.reasoning_tokens)})` : ""}`],
    ["Rate-limited responses / circuit opens", `${n(b.rate_limited_responses)} / ${n(b.circuit_opens)}`],
    ["Queue wait p95", el("span",{}, b.queue_wait_p95_ms==null ? "—" : `${int(b.queue_wait_p95_ms)} ms`, b.client_disconnects ? el("span",{class:"warn-text",text:` · SUT connections closed while queued/retrying: ${n(b.client_disconnects)} times (concurrency may be too low or its HTTP timeout too short)`}) : null)],
    ["By episode", (b.by_episode||[]).length ? `${b.by_episode.length} episodes; largest single-episode prompt ${int(Math.max(...b.by_episode.map(e=>e.prompt_tokens||0)))}` : "—"]]));
}
function containerPanel(iso, enforced, kv){
  const c = iso.container||{}, rt = c.runtime||{}, img = c.image||{}, net = c.network||{}, res = c.residue||{}, lim = c.limits_enforced||{};
  const count = n => n==null ? "—" : String(n);
  return el("div",{}, el("h3",{text:"Container runtime (Tier D)"}), kv([
    ["Runtime", `${rt.client||"—"} / Server ${rt.server||"—"} · ${rt.os||"—"}/${rt.arch||"—"} · Kernel ${rt.kernel||"—"} · context ${rt.context||"—"}`],
    ["Image", el("span",{}, el("code",{text: img.id ? short(img.id.replace(/^sha256:/,"")) : "—"}), img.ref ? ` ${img.ref}` : "")],
    ["Sidecar images", (c.sidecar_images||[]).length ? (c.sidecar_images||[]).map(s => typeof s==="string" ? s : (s.ref||s.image||"")).join(", ") : "—"],
    ["Network", el("span",{}, `${net.mode||iso.network_mode||"—"}${net.internal ? " · internal" : ""}${net.name ? ` · ${net.name}` : ""} `, enforced(iso.network_enforced))],
    ["Egress allowlist", Array.isArray(iso.egress_allowlist) ? (iso.egress_allowlist.length ? el("code",{text: iso.egress_allowlist.join(", ")}) : "None (zero egress)") : (iso.network_mode==="open" ? "Unrestricted (open)" : "—")],
    ["LLM gateway relay", c.relay ? el("span",{}, el("code",{text: c.relay.target||"—"}), ` ← ${c.relay.listen||"—"} · ${c.relay.container||"—"}`) : "—"],
    ["Gateway governor", c.governor ? el("span",{}, el("code",{text: c.governor.target||"—"}), ` · Concurrency ${c.governor.concurrency??"—"} · rpm ${c.governor.rpm||"Unlimited"} · Budget ${c.governor.budget_tokens ? int(c.governor.budget_tokens)+" tokens" : "Unlimited"} · Circuit opens ${(c.governor.circuit_events||[]).length} times`) : "—"],
    ["User", `${c.user||"—"}${c.user_mode ? ` (${c.user_mode})` : ""}`],
    ["Security flags", el("code",{text:(c.flags||[]).join(" ")||"—"})],
    ["Resource limits", `Timeout ${lim.timeout===true?"Yes":lim.timeout===false?"No":"—"} · Memory ${lim.max_rss===true?"Yes":lim.max_rss===false?"No":"—"} · Processes ${lim.pids_max===true?"Yes":lim.pids_max===false?"No":"—"}`],
    ["Published host ports", c.host_ports_published===false ? statusIcon("good","None") : c.host_ports_published===true ? statusIcon("bad","Yes") : statusIcon("na","Unknown")],
    ["Residue verification", el("span",{}, enforced(res.run_clean), ` Containers ${count(res.containers_remaining)} · Network ${count(res.networks_remaining)} · Volumes ${count(res.volumes_remaining)} · Retained EP ${(res.kept_eps||[]).length}`)]]));
}
function disclosureCard(r){
  const iso = r.isolation||{}, p = r.protocol||{}, fp = (r.sut||{}).fingerprint||{};
  const enforced = v => v===true ? statusIcon("good","Enforced") : v===false ? statusIcon("bad","Not enforced") : statusIcon("na","Not applicable");
  const kv = pairs => el("dl",{class:"kv"}, pairs.flatMap(([k,v]) => [el("dt",{text:k}), el("dd",{}, v)]));
  const fpRows = Object.keys(fp).sort().map(k => el("tr",{}, el("td",{text:k}), el("td",{}, fp[k].status==="declared" ? statusIcon("good","Declared") : fp[k].status==="observed" ? statusIcon("good","Observed") : statusIcon("na","Unavailable")), el("td",{class: fp[k].value==null?"null":"",text: fp[k].value==null ? "—" : String(fp[k].value)})));
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:"Isolation, protocol, and SUT fingerprint"})), el("div",{class:"grid2"},
    el("div",{}, el("h3",{text:"Isolation scope"}), kv([["Tier", `Tier ${iso.tier||"—"} · ${iso.backend||"—"}`],["Filesystem isolation", enforced(["A","B","D"].includes(iso.tier))],["Host-access restriction", enforced(["A","B","D"].includes(iso.tier))],["External network", iso.tier==="C" ? el("span",{class:"muted",text:"In-process baseline, no network calls (isolation not required)"}) : el("span",{}, `${({none:"none (Zero egress)",gateway:"gateway (LLM gateway only)",open:"open (Unrestricted)"})[iso.network_mode]||iso.network_mode||"—"} `, enforced(iso.network_enforced))],["SUT LLM", (r.sut||{}).llm ? `${(r.sut.llm.gateway_host||"—")} · ${(r.sut.llm.model||"—")}${r.sut.llm.reasoning_effort ? ` · effort ${r.sut.llm.reasoning_effort}` : ""}${r.sut.llm.source ? ` (${r.sut.llm.source})` : ""}` : "—"],["Remote state", enforced(null)],["Sandbox profile", el("code",{text: iso.sandbox_profile_sha256 ? short(iso.sandbox_profile_sha256) : "—"})],["Selftest", el("code",{text: iso.selftest ? short(iso.selftest.sha256||JSON.stringify(iso.selftest)) : "—"})],["Host", `${(iso.host||{}).os||""} ${(iso.host||{}).arch||""} ${(iso.host||{}).kernel||""}`]])),
    el("div",{}, el("h3",{text:"Run protocol"}), kv([["Version", p.version||"—"],["Benchmark protocol", benchmarkCell(p.benchmark)],["Budget", `top_k=${(p.budget||{}).top_k} items · Context ≤ ${int((p.budget||{}).max_context_chars)} characters`],["Ingestion unit", p.ingestion||"—"],["Reset policy", p.reset_policy||"—"],["K unit", el("code",{text:p.k_unit||"—"})],["gold stays inside evaluator", p.gold_hidden_from_adapter ? statusIcon("good","Yes") : statusIcon("bad","No")],["Changes from v1", el("ul",{style:"margin:0;padding-left:16px"}, (p.changes_from_v1||[]).map(c => el("li",{text:c})))]]))),
    iso.container ? containerPanel(iso, enforced, kv) : null,
    r.data_flow ? dataFlowPanel(r.data_flow, enforced) : null,
    (r.billing||{}).sut ? sutBillingPanel(r.billing.sut, kv) : null,
    el("h3",{text:"SUT fingerprint (declared / observed / unavailable)"}),
    el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Field"}), el("th",{text:"Source"}), el("th",{text:"Value"}))), el("tbody",{}, fpRows))),
    el("p",{class:"muted",text:"Direct SUT provider traffic is not intercepted; its model identity and cost are reported only when declared or self-reported."}));
}

function casesCard(r){
  const cases = r.cases||[]; let filter = "all", q = "";
  const list = el("div",{});
  const chip = (key,label) => el("button",{"aria-pressed": key==="all" ? "true":"false", text:label, onclick:(e)=>{ filter=key; for (const b of chips.querySelectorAll("button")) b.setAttribute("aria-pressed", b===e.currentTarget); render(); }});
  const chips = el("div",{class:"filters"}, chip("all","All"), chip("pass","Pass"), chip("fail","Fail"), chip("unscored","Unscored / unsupported"), chip("error","Error"), el("input",{type:"search",placeholder:"Search case ID / category",oninput:(e)=>{ q=e.target.value.trim().toLowerCase(); render(); }}));
  const count = el("small",{});
  function match(c){ if (q && !(c.case_id.toLowerCase().includes(q) || (c.category||"").toLowerCase().includes(q))) return false; if (filter==="pass") return c.status==="scored" && c.passed===true; if (filter==="fail") return c.status==="scored" && c.passed===false; if (filter==="unscored") return c.status==="unscored"||c.status==="unsupported"; if (filter==="error") return ["error","contract_error","ingest_failed"].includes(c.status); return true; }
  function render(){ const rows = cases.filter(match); count.textContent = `${rows.length} / ${cases.length} rows`;
    list.replaceChildren(el("div",{class:"casehead"}, el("span",{text:"Case"}), el("span",{text:"Category"}), el("span",{text:"Status"}), el("span",{text:"Assertions / recall"}), el("span",{text:"Returned characters"})), ...rows.map(c => {
      const m = c.metrics||{}; const metricTxt = Object.keys(m).filter(k=>isNum(m[k])).map(k=>`${k}=${num(m[k],3)}`).join("  ");
      return el("details",{class:"case"}, el("summary",{}, el("span",{}, el("span",{class:"verdict "+statusCls(c)}), el("span",{class:"mono",text:c.case_id})), el("span",{class:"ink2",text:c.category||"—"}), el("span",{class:"ink2",text: c.status==="scored" ? (c.passed===true?"Pass":c.passed===false?"Fail":"Scored") : (STATUS_ZH[c.status]||c.status)}), el("span",{class:"tnum ink2",text: c.assertions ? `${c.assertions_passed}/${c.assertions}` : (isNum(m.recall) ? `R ${num(m.recall,2)}` : "—")}), el("span",{class:"tnum ink2",text:int(c.returned_chars)})),
        el("div",{class:"body"}, (c.failures||[]).length ? el("div",{}, el("b",{text:"Failed assertions: "}), el("ul",{style:"margin:4px 0;padding-left:16px"}, c.failures.map(f=>el("li",{text:f})))) : null, c.note ? el("p",{text:c.note}) : null, c.error_type ? el("p",{}, el("b",{text:"Error type: "}), c.error_type) : null,
          el("div",{}, el("b",{text:`Returned sources (${(c.retrieved_source_ids||[]).length}): `}), el("code",{text:(c.retrieved_source_ids||[]).join("  ")||" (Empty)"})),
          metricTxt ? el("div",{}, el("b",{text:"Metric: "}), el("code",{text:metricTxt})) : null,
          el("div",{class:"muted"}, `cutoff=${c.cutoff} · cross_user=${(c.diagnostics||{}).cross_user_source_count ?? 0} · duplicate=${(c.diagnostics||{}).duplicate_source_occurrences ?? 0} · repeat ${c.repeat_index} · query ${num((c.timings||{}).query_ms,2)} ms`)));
    })); }
  render();
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:"Case diagnostics"}), count), chips, list);
}

function excludedStatusTable(){
  const counts = compare.excluded_by_status;
  if (!counts) return null;
  const sides = ["left", "right", "both"];
  const statuses = [...new Set(sides.flatMap(side => Object.keys(counts[side]||{})))].sort();
  const rows = statuses.map(status => el("tr",{}, el("td",{text:`${STATUS_ZH[status]||status} (${status})`}),
    ...sides.map(side => el("td",{class:"num",text:int((counts[side]||{})[status]||0)}))));
  return el("div",{id:"excluded-by-status"}, el("h3",{text:"Explicit status exclusions (case × repeat)"}),
    statuses.length ? el("div",{class:"scroll"}, el("table",{class:"tbl"},
      el("thead",{}, el("tr",{}, el("th",{text:"Status"}), el("th",{class:"num",text:`Left only · ${compare.left}`}), el("th",{class:"num",text:`Right only · ${compare.right}`}), el("th",{class:"num",text:"Both"}))),
      el("tbody",{}, rows))) : el("p",{class:"muted",text:"No samples were excluded by status."}),
    el("p",{class:"muted",text:"Left-only/right-only means the other side was scored; both means both statuses are explicitly excluded. Different excluded statuses count in both status rows. Deltas and confidence intervals apply only to retained paired samples."}));
}

function compareCard(){
  if (!compare || reports.length<2) return null;
  const left = reports.find(r=>sutName(r).startsWith(compare.left)) || reports[0], right = reports.find(r=>sutName(r).startsWith(compare.right)) || reports[1];
  const lm = new Map((left.cases||[]).map(c=>[c.case_id,c])); const flips = (right.cases||[]).filter(c => lm.has(c.case_id) && lm.get(c.case_id).status==="scored" && c.status==="scored" && lm.get(c.case_id).passed!==c.passed);
  const ci = compare.ci95 ? `[${num(compare.ci95[0],3)}, ${num(compare.ci95[1],3)}]` : "—  (One cluster; no population confidence interval)";
  const d = compare.delta_right_minus_left;
  return el("section",{class:"card"}, el("header",{}, el("h2",{text:`Paired comparison: ${compare.right} − ${compare.left}`}), el("small",{text:`Metric ${compare.metric}`})),
    el("div",{class:"kpis"}, el("div",{class:"tile"}, el("div",{class:"lbl",text:"Paired delta (right − left)"}), el("div",{class:"val hero",style:isNum(d)?`color:${d>=0?"var(--good-text)":"var(--critical)"}`:null,text:(isNum(d)&&d>0?"+":"")+num(d,3)}), el("div",{class:"sub",text:`Paired cases ${int(compare.paired_cases)} · Clusters ${int(compare.clusters)}`})),
      el("div",{class:"tile"}, el("div",{class:"lbl",text:"95% Confidence interval"}), el("div",{class:"val"+(compare.ci95?"":" note"),text:ci}), el("div",{class:"sub",text:compare.note||""})),
      el("div",{class:"tile"}, el("div",{class:"lbl",text:"Changed judgments"}), el("div",{class:"val",text:int(flips.length)}), el("div",{class:"sub",text:compare.excluded_by_status ? `Excluded: ${(compare.excluded_cases||[]).length} cases (excluded status or missing metric)` : `Excluded ${(compare.excluded_cases||[]).length} cases without a valid assertion`}))),
    excludedStatusTable(),
    (compare.protocol_differences||[]).length ? el("div",{}, el("h3",{text:"Protocol differences (pairing is valid; disclose these with results)"}), el("ul",{class:"muted",style:"margin:0;padding-left:16px"}, compare.protocol_differences.map(x => el("li",{text:x})))) : null,
    flips.length ? el("div",{class:"scroll"}, el("table",{class:"tbl"}, el("thead",{}, el("tr",{}, el("th",{text:"Case"}), el("th",{text:"Category"}), el("th",{text:compare.left}), el("th",{text:compare.right}))), el("tbody",{}, flips.map(c => el("tr",{}, el("td",{class:"mono",text:c.case_id}), el("td",{text:c.category||"—"}), el("td",{text: lm.get(c.case_id).passed ? "Pass":"Fail"}), el("td",{text: c.passed ? "Pass":"Fail"})))))) : el("p",{class:"muted",text:"No cases changed pass/fail judgment."}));
}

function sutTiles(){
  return el("div",{class:"kpis"}, reports.map((r,i) => { const s=r.summary||{}; const mo=(r.scores||{}).memory_only||{}; const iso=r.isolation||{}; const hasPass=isNum(s.pass_rate_on_scored);
    const head = hasPass ? pct(s.pass_rate_on_scored) : pct(mo.recall && mo.recall.mean);
    const sub = hasPass ? `${(r.mode==="fixed_reader"||r.mode==="native_answer")?"Correct":"Pass"} ${int(s.passed)} / ${int(s.n_with_pass_judgment)} · Recall ${pct(mo.recall && mo.recall.mean)} · Empty result ${pct(mo.empty_result_correct && mo.empty_result_correct.mean)}` : `Recall@K (n=${mo.recall ? mo.recall.n : "—"})· No pass judgment · Empty result ${pct(mo.empty_result_correct && mo.empty_result_correct.mean)}`;
    return el("div",{class:"tile"}, el("div",{class:"lbl"}, el("span",{class:"sw",style:`display:inline-block;width:12px;height:12px;border-radius:3px;background:${SERIES[i]};vertical-align:-2px;margin-right:6px`}), sutName(r)), el("div",{class:"val",text:head}), el("div",{class:"sub",text:sub}), el("div",{class:"sub",text:`Isolation Tier ${iso.tier||"?"} · ${iso.backend||"—"} · Network ${iso.network_mode||"—"}${iso.network_enforced?"":" (Not enforced)"}`})); }));
}

function footer(r){
  return el("footer",{}, `run ${r.run_id||"—"} · suite sha256 ${short(r.suite_sha256)} · code sha256 ${short((r.environment||{}).code_sha256)} · Framework ${r.framework_version||"—"}. Generated from report.json with no external resources; raw JSON / JSONL / CSV retain all metrics, nulls, and per-case evidence.`);
}

/* ---------- mount ---------- */
(function init(){
  try { const qs = new URLSearchParams(location.search).get("theme"); const saved = qs || localStorage.getItem("memeval-theme"); if (saved) document.documentElement.setAttribute("data-theme", saved); } catch(e){}
  const app = document.getElementById("app"); if (!reports.length) { app.append(el("p",{text:"No report data."})); return; }
  app.append(header(), notice());
  if (reports.length > 1) {
    app.append(sutTiles(), categoryCard(reports), compareCard());
    const picker = el("div",{class:"filters"}, el("span",{class:"muted",text:"Inspect one system:"}), ...reports.map((r,i)=>el("button",{"aria-pressed": i===0?"true":"false", text:sutName(r), onclick:(e)=>{ for (const b of picker.querySelectorAll("button")) b.setAttribute("aria-pressed", b===e.currentTarget); detail.replaceChildren(...detailOf(r)); }})));
    const detail = el("div",{}, ...detailOf(reports[0]));
    app.append(picker, detail);
  } else {
    app.append(kpis(reports[0]), categoryCard(reports), ...detailOf(reports[0]).slice(1));
  }
  app.append(footer(reports[0]));
  function detailOf(r){ return [kpis(r), scoresCard(r), timingsCard(r), disclosureCard(r), casesCard(r)]; }
})();
})();
