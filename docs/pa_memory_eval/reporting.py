"""Self-contained report, deterministic artifacts, and honest paired comparison."""
from __future__ import annotations
import csv
import html
import json
from pathlib import Path
import random
from statistics import mean
from .core import ContractError, write_json
from .metrics import percentile


def number(v, pct=False):
    if v is None: return "—"
    return f"{100*v:.1f}%" if pct else f"{v:.3f}"


def report_html(reports: list[dict]) -> str:
    esc = lambda x: html.escape(str(x))
    cards = ""; details = ""; metrics_table = ""; quantitative = ""
    for r in reports:
        s=r["summary"]; name=r["adapter"]["name"]
        cards += f'<section class="card"><div class="eyebrow">{esc(name)}</div><h2>{number(s["pass_rate_on_scored"], True)}</h2><p>通过 {s["passed"]} / 有效判定 {s["n_with_pass_judgment"]}</p><p>运行 {s["planned"]} · 错误 {s["execution_errors"]} · 不支持 {s["unsupported"]}</p><small>仅来源特定诊断，不是通用 PA 总分</small></section>'
        for metric, measured in s["metrics"].items():
            quantitative += f'<tr><td>{esc(name)}</td><td>{esc(metric)}</td><td>{number(measured["mean"])}</td><td>{measured["n"]}</td></tr>'
        for category, g in r["by_category"].items():
            metrics_table += f'<tr><td>{esc(name)}</td><td>{esc(category)}</td><td>{g["planned"]}</td><td>{g["passed"]}/{g["n_with_pass_judgment"]}</td><td>{number(g["pass_rate_on_scored"],True)}</td><td>{number(g["timings"]["query_ms"]["p50"])}</td></tr>'
        for row in r["cases"]:
            label = "通过" if row.get("passed") else "未通过" if row.get("passed") is False else row["status"]
            problems = "；".join(row.get("failures", [])) or row.get("note") or "所有有效断言均通过"
            details += f'<details class="case" data-search="{esc((name+" "+row["case_id"]+" "+row["category"]).lower())}"><summary><span class="tag {"ok" if row.get("passed") else ""}">{esc(label)}</span> <b>{esc(name)}</b> / {esc(row["case_id"])}</summary><p>{esc(row["category"])}</p><p>{esc(problems)}</p><code>{esc(json.dumps(row.get("retrieved_source_ids",[]),ensure_ascii=False))}</code></details>'
    first=reports[0]
    src=first["source"]
    is_pmb = first["suite_id"].startswith("precisionmembench")
    scope_notice = ("公开源 PrecisionMemBench 的 35 条结构化记忆与 77 条单轮诊断投影，其中 1 条无有效断言而不计分；用例由上游作者构造，并非真人聊天记录，也不是完整官方协议。" if is_pmb else f"来源：{first['suite_id']}；当前报告包含 {first['summary']['planned']} 条计划用例；评分与未评分条数请见摘要，不能视作官方榜单成绩。")
    run_boundary = ("本页未执行付费模型、完整公开 QA 数据集或真实 PA 系统，所以这些结果均未出现在本页榜单中。" if is_pmb else "模型、适配器、模式与数据哈希记录于 JSON 报告；只解读实际生成的非空指标。")
    notes="".join(f"<li>{esc(x)}</li>" for x in first.get("source_limitations",[]))
    return f'''<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>PA Memory Eval · 实测诊断</title>
<style>
:root{{color-scheme:dark;--bg:#10151d;--panel:#18212c;--line:#2c3a4a;--text:#edf3f8;--muted:#9cafc1;--accent:#88dfcc}}*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--text);font:15px/1.65 system-ui,-apple-system,"PingFang SC",sans-serif}}main{{max-width:1280px;margin:auto;padding:48px 28px 80px}}h1{{font-size:clamp(28px,5vw,45px);line-height:1.2;letter-spacing:-1px}}h2{{font-size:34px;margin:12px 0}}h3{{font-size:23px;margin:44px 0 16px}}p{{margin:8px 0}}.eyebrow{{letter-spacing:2px;text-transform:uppercase;color:var(--accent);font-size:12px;font-weight:700}}.intro{{max-width:850px;color:var(--muted)}}.cards{{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px;margin-top:28px}}.card{{padding:24px;background:var(--panel);border:1px solid var(--line);border-radius:14px}}small{{color:var(--muted)}}.notice{{margin-top:25px;border-left:3px solid var(--accent);padding:16px 22px;background:var(--panel)}}a{{color:var(--accent)}}.scroll{{overflow-x:auto;border:1px solid var(--line);border-radius:12px}}table{{border-collapse:collapse;width:100%;min-width:670px}}td,th{{padding:12px 16px;text-align:left;border-bottom:1px solid var(--line)}}th{{background:var(--panel);color:var(--muted);font-size:13px}}tr:last-child td{{border:0}}code{{font-family:ui-monospace,monospace;font-size:12px;overflow-wrap:anywhere}}input{{width:100%;padding:14px 16px;border-radius:9px;background:var(--panel);border:1px solid var(--line);color:var(--text);font:inherit;margin:0 0 18px}}details.case{{padding:14px 16px;border:1px solid var(--line);border-radius:9px;margin:8px 0}}summary{{cursor:pointer}}.tag{{display:inline-block;font-size:11px;background:#57392d;padding:2px 9px;border-radius:5px}}.tag.ok{{background:#1c4c45}}.meta{{word-break:break-all;color:var(--muted)}}footer{{margin-top:50px;font-size:12px;color:var(--muted)}}[hidden]{{display:none!important}}@media(max-width:600px){{main{{padding:28px 16px}}}}
</style><main><div class="eyebrow">SOURCE-GROUNDED / v0.1.0</div><h1>Personal Agent<br>记忆评价 · 实测诊断</h1><p class="intro">按来源区分能力，按用例保存证据；不以模型自述、演示数据或单一综合分代替测量。</p><div class="notice"><b>本页实际运行：</b>{esc(scope_notice)}</div><div class="cards">{cards}</div>
<h3>测量范围</h3><p>报告时间取运行环境时钟，检索快照日期为 {esc(first["research_snapshot_date"])}；本地毫秒耗时不代表云端 Agent 的性能。</p><p>{esc(run_boundary)}</p><p>零记忆可能通过负向或空结果断言，必须和正向召回、覆盖率一起解读。</p>
<h3>分母明确的指标</h3><div class="scroll"><table><thead><tr><th>基线</th><th>指标</th><th>均值</th><th>有标签的题数</th></tr></thead><tbody>{quantitative}</tbody></table></div><h3>分类结果</h3><div class="scroll"><table><thead><tr><th>基线</th><th>来源分类</th><th>用例</th><th>通过/有效</th><th>通过率</th><th>查询 p50/ms</th></tr></thead><tbody>{metrics_table}</tbody></table></div>
<h3>来源与可重复性</h3><p><a href="{esc(src.get("source_url", ""))}" target="_blank" rel="noreferrer noopener">打开原始仓库</a></p><p class="meta">Revision: {esc(src.get("revision",src.get("source_revision","not pinned")))}</p><p class="meta">Normalized suite SHA-256: {esc(first["suite_sha256"])}</p><p>{esc(src.get("acquisition","Local source import"))}</p><ul>{notes}</ul>
<h3>逐例诊断</h3><input id="q" placeholder="按项目、用例 ID 或分类筛选…" aria-label="筛选测试结果">{details}<footer>PA Memory Eval / Source-specific diagnostic / 所有统计由 reports/*.json 生成 / 无外部脚本、字体或追踪器</footer></main><script>document.getElementById('q').addEventListener('input',e=>{{const q=e.target.value.toLowerCase();document.querySelectorAll('.case').forEach(n=>n.hidden=!n.dataset.search.includes(q))}})</script></html>'''


def save_run(report, directory):
    root=Path(directory); root.mkdir(parents=True, exist_ok=True)
    write_json(root/"report.json", report)
    (root/"cases.jsonl").write_text("".join(json.dumps(x,ensure_ascii=False,allow_nan=False)+"\n" for x in report["cases"]),encoding="utf-8")
    with open(root/"cases.csv","w",encoding="utf-8-sig",newline="") as f:
        keys=["case_id","category","status","passed","returned_chars"]
        writer=csv.DictWriter(f,fieldnames=keys+["query_ms","failures"]); writer.writeheader()
        for row in report["cases"]:
            writer.writerow({**{k:row.get(k) for k in keys},"query_ms":row["timings"].get("query_ms"),"failures":" | ".join(row.get("failures",[]))})
    (root/"report.html").write_text(report_html([report]),encoding="utf-8")
    predictions=[{"question_id":x["case_id"],"hypothesis":x["prediction"]} for x in report["cases"] if "prediction" in x]
    if predictions:
        (root/"predictions.jsonl").write_text("".join(json.dumps(x,ensure_ascii=False)+"\n" for x in predictions),encoding="utf-8")
    for p in root.iterdir():
        if p.is_file(): p.chmod(0o600)


def compare_runs(left, right, *, metric="pass", seed=42, draws=2000):
    if left["suite_sha256"]!=right["suite_sha256"] or left["mode"]!=right["mode"]:
        raise ContractError("Paired comparison requires identical suite hash and mode")
    for key in ("budget","batch_size","scoring","case_limit"):
        if left["protocol"].get(key)!=right["protocol"].get(key): raise ContractError("Protocol differs: "+key)
    if left.get("reader") != right.get("reader"): raise ContractError("Fixed reader differs")
    a={r["case_id"]:r for r in left["cases"]}; b={r["case_id"]:r for r in right["cases"]}
    if set(a)!=set(b): raise ContractError("Case coverage differs")
    groups={}
    excluded=[]
    for cid,x in a.items():
        y=b[cid]
        if x["status"] != y["status"]: raise ContractError("Execution/scoring coverage differs; do not drop failed cases")
        if x["status"]!="scored": excluded.append(cid); continue
        ax=x.get("passed") if metric=="pass" else x.get("metrics",{}).get(metric)
        by=y.get("passed") if metric=="pass" else y.get("metrics",{}).get(metric)
        if ax is None or by is None: excluded.append(cid); continue
        groups.setdefault(x["cluster_id"],[]).append(float(by)-float(ax))
    if not groups: raise ContractError("No paired measurements")
    observed=mean(v for values in groups.values() for v in values)
    out={"left":left["adapter"]["name"],"right":right["adapter"]["name"],"metric":metric,"delta_right_minus_left":observed,
         "paired_cases":sum(map(len,groups.values())),"clusters":len(groups),"excluded_cases":excluded,"ci95":None}
    if len(groups)<2:
        out["note"]="One shared source corpus: no population-level confidence interval is reported"
    else:
        rng=random.Random(seed); keys=sorted(groups); boot=[]
        for _ in range(draws):
            picks=[rng.choice(keys) for _ in keys]
            boot.append(mean(v for k in picks for v in groups[k]))
        out.update(ci95=[percentile(boot,.025),percentile(boot,.975)], bootstrap="paired cluster percentile",draws=draws,seed=seed)
    return out
