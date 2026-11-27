#!/usr/bin/env python3
"""Inline CSS/JS/data into shell.html -> prototype pages (design-time only; Go embeds the same assets)."""
import json, pathlib, sys
here = pathlib.Path(__file__).parent
root = here.parent.parent
css = (here/"report.css").read_text(); js = (here/"report.js").read_text(); shell = (here/"shell.html").read_text()
def build(reports, compare, out):
    data = {"reports":[json.load(open(root/p)) for p in reports], "compare": json.load(open(root/compare)) if compare else None}
    blob = json.dumps(data, ensure_ascii=False).replace("</", "<\\/")
    html = shell.replace("/*CSS*/", css).replace("/*JS*/", js).replace("/*DATA*/", blob)
    (here/out).write_text(html); print(out, len(html), "bytes")
build(["reports/m0-bm25/report.json"], None, "prototype-single.html")
build(["reports/m0-bm25/report.json","reports/m0-recent/report.json","reports/m0-null/report.json"], "docs/reports/paired-null-bm25.json", "prototype-compare.html")
