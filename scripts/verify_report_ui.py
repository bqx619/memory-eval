# Historical host-era acceptance recipe, not a current execution entry point.
# Bundled lock.json paths are sanitized fixtures; current SUTs use container Lock v3.
"""Generate reports and verify the unchanged UI in offline local Chrome."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import subprocess
import tempfile
from textwrap import dedent

root = Path(__file__).resolve().parents[1]
def main(args, work):
    os.chdir(root)
    out = work.out
    workspace = work.workspace()
    cli = str(work.binary())
    chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    env = work.env
    def run(args, name):
        cwd = root
        if args[0] == cli:
            args = work.cli(*args[1:])
            cwd = workspace
        p = subprocess.run(args, cwd=cwd, text=True, capture_output=True, env=env, timeout=None if args[0] == 'go' else 60)
        (out / name).write_text(p.stdout + p.stderr)
        if p.returncode:
            raise RuntimeError(f"exit {p.returncode}: {args}; see {out / name}")
        return p.stdout

    run(["go", "vet", "./..."], "vet.log")
    lock = json.loads((root / "suts/octop-memory/lock.json").read_text())
    env["MEMEVAL_OCTOP_VENV"] = lock["venv_real_path"]
    run(["go", "test", "-count=1", "-json", "./..."], "tests.jsonl")
    run([cli, "sandbox", "selftest", "--sut", str(workspace / "suts/octop-memory/manifest.json"), "--out", str(workspace / "reports")], "selftest-after-ui.json")

    # Chrome cannot install its child Seatbelt policies inside a pre-existing
    # Seatbelt policy on this host. Disable only its inner browser sandbox; the
    # OUTER OS policy below remains active for every browser process and blocks
    # all IP networking. Unix sockets are required by the private Chrome profile.
    policy = "(version 1) (allow default) (deny network*) (allow network-bind network-inbound network-outbound (local unix-socket) (remote unix-socket))\n"
    (out / "chrome-offline.sb").write_text(policy)
    probe = dedent('''
    import errno,json,socket
    results=[]
    for family,address in [(socket.AF_INET,('127.0.0.1',9)),(socket.AF_INET6,('::1',9))]:
     for kind in [socket.SOCK_STREAM,socket.SOCK_DGRAM]:
      try:
       with socket.socket(family,kind) as s:
        s.settimeout(0.2)
        if kind==socket.SOCK_STREAM: s.connect(address)
        else: s.sendto(b'offline-probe',address)
      except OSError as e: results.append({'family':int(family),'kind':int(kind),'errno':e.errno,'blocked':e.errno in [errno.EPERM,errno.EACCES]})
      else: results.append({'family':int(family),'kind':int(kind),'blocked':False})
    assert all(r['blocked'] for r in results),results
    print(json.dumps(results))
    ''')
    network_probe = json.loads(run(["/usr/bin/sandbox-exec", "-f", str(out / "chrome-offline.sb"), sys.executable, "-B", "-c", probe], "network-denial.json"))

    asset_hashes = {}
    for name in ["report.css", "report.js", "shell.html"]:
        design = (root / "design/report-ui" / name).read_bytes()
        embedded = (root / "internal/report/assets" / name).read_bytes()
        assert design == embedded
        asset_hashes[name] = hashlib.sha256(design).hexdigest()

    class DOM(HTMLParser):
        def __init__(self):
            super().__init__()
            self.tiles = self.svgs = self.images = 0
            self.external = []
            self.in_data = False
            self.data = []
        def handle_starttag(self, tag, attrs):
            attrs = dict(attrs)
            self.tiles += "tile" in attrs.get("class", "").split()
            self.svgs += tag == "svg"
            self.images += tag == "img"
            if tag == "script" and attrs.get("id") == "memeval-data": self.in_data = True
            if tag in ["script", "img", "iframe", "link", "source", "video", "audio"]:
                for key in ["src", "href"]:
                    if attrs.get(key): self.external.append(attrs[key])
        def handle_endtag(self, tag):
            if tag == "script": self.in_data = False
        def handle_data(self, text):
            if self.in_data: self.data.append(text)

    pages = []
    inputs = [root / f"reports/m0-{v}/report.json" for v in ["bm25", "recent", "null"]]
    inputs += sorted((root / "reports").glob("m1b-*/report.json"))
    input_hashes = {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in inputs}
    for source in inputs:
        target = out / source.parent.name / "report.html"
        run([cli, "report", str(source), "--out", str(target)], source.parent.name + "-generate.json")
        pages.append((source.parent.name + "-report", target, False))

    m0_delta = str(out / "m0-delta.json")
    run([cli, "compare", str(root / "reports/m0-null/report.json"), str(root / "reports/m0-bm25/report.json"), "--out", m0_delta], "m0-compare.json")
    target = out / "m0-comparison/comparison.html"
    run([cli, "report", *[str(root / f"reports/m0-{v}/report.json") for v in ["bm25", "recent", "null"]], "--compare", m0_delta, "--out", str(target)], "m0-comparison-generate.json")
    pages.append(("m0-comparison", target, True))
    for baseline in ["bm25", "recent", "null"]:
        target = out / f"m1b-octop-vs-{baseline}/comparison.html"
        run([cli, "report", str(root / "reports/m1b-octop-a/report.json"), str(root / f"reports/m1b-{baseline}/report.json"), "--compare", str(root / f"reports/m1b-octop-vs-{baseline}/delta.json"), "--out", str(target)], f"m1b-octop-vs-{baseline}-generate.json")
        pages.append((f"m1b-octop-vs-{baseline}-comparison", target, True))

    # Exercise parser-sensitive strings in the browser, not merely Go's JSON parser.
    injection = json.loads(inputs[0].read_text())
    payload = '</ScRiPt><img src=x onerror="document.body.dataset.injected=1"><!--<script>/*CSS*//*DATA*/'
    injection["sut"]["name"] = payload
    injection["sut"]["variant"] = payload
    (out / "injection.json").write_text(json.dumps(injection, ensure_ascii=False))
    target = out / "injection-report.html"
    run([cli, "report", str(out / "injection.json"), "--out", str(target)], "injection-generate.json")
    pages.append(("injection-report", target, False))

    checks = []
    for name, target, comparison in pages:
        screenshot = out / f"{name}.png"
        screenshot.unlink(missing_ok=True)
        with tempfile.TemporaryDirectory(prefix="chrome-", dir=out) as profile:
            args = ["/usr/bin/sandbox-exec", "-f", str(out / "chrome-offline.sb"), chrome,
                "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-networking",
                "--disable-component-update", "--disable-domain-reliability", "--disable-sync",
                "--no-first-run", "--no-default-browser-check", "--disable-extensions",
                "--disable-features=MediaRouter,OptimizationHints", "--disable-quic",
                "--host-resolver-rules=MAP * ~NOTFOUND", "--proxy-server=http://127.0.0.1:9",
                "--user-data-dir=" + profile, "--window-size=1440,1800", "--virtual-time-budget=3000", "--run-all-compositor-stages-before-draw",
                "--disable-threaded-animation", "--disable-threaded-scrolling", "--disable-checker-imaging",
                "--dump-dom", "--log-net-log=" + str(out / f"{name}.netlog.json"),
                "--screenshot=" + str(screenshot), target.resolve().as_uri()]
            p = subprocess.run(args, text=True, capture_output=True, timeout=60)
            (out / f"{name}.command.json").write_text(json.dumps(args, ensure_ascii=False, indent=2))
            (out / f"{name}.dom.html").write_text(p.stdout)
            (out / f"{name}.chrome.log").write_text(p.stderr)
            assert p.returncode == 0, (name, p.returncode, p.stderr)
        dom = DOM()
        dom.feed(p.stdout)
        assert dom.tiles > 0 and dom.svgs > 0 and not dom.external, (name, vars(dom))
        assert screenshot.read_bytes().startswith(b"\x89PNG\r\n\x1a\n")
        data = json.loads("".join(dom.data))
        assert (data["compare"] is not None) == comparison
        if comparison: assert "<h2>Paired comparison: " in p.stdout
        if name == "injection-report":
            assert data["reports"][0]["sut"]["name"] == payload and dom.images == 0
            assert 'data-injected="1"' not in p.stdout
        netlog = json.loads((out / f"{name}.netlog.json").read_text())
        names = {v: k for k, v in netlog["constants"]["logEventTypes"].items()}
        sent = [e for e in netlog["events"] if names.get(e["type"]) in ["SOCKET_BYTES_SENT", "UDP_BYTES_SENT"]]
        assert not sent, (name, "network payload sent", sent)
        checks.append({"page": str(target), "tiles": dom.tiles, "svgs": dom.svgs, "comparison": comparison,
                       "screenshot": str(screenshot), "network_bytes_sent_events": len(sent)})
    assert input_hashes == {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in inputs}
    receipt = {"status": "passed", "assets_byte_identical": asset_hashes, "checks": checks,
               "input_report_json_unchanged": True, "network_policy": policy, "network_probe": network_probe,
               "browser_inner_sandbox_disabled": True, "browser_outer_seatbelt_active": True,
               "json_escape_assumption": "Use valid JSON Unicode escape \\u003c!-- instead of invalid <\\!--; closing tags use <\\/",
               "designer_renderer_limitations": ["retrieval passed:null is displayed as 失败 by unchanged casesCard/statusCls", "retrieval category assertion rates have no pass denominator and display —; recall metrics remain visible"]}
    (out / "acceptance.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n")
    print(f"报告 UI 验收通过：{len(checks)} 页均有 tile 与 SVG，截图和 DOM 留在 {out}。")
    print("三份设计资源逐字节一致；JSON 注入/compare/恶意闭合标签浏览器验证通过。")
    print("Chrome 外层 Seatbelt 拒绝 TCP/UDP IPv4/IPv6；全部 netlog 的发送字节事件为 0。")
    print("go vet/test 通过（含真实 octop 契约）；最终二进制 selftest 已刷新。")

if __name__ == '__main__':
    verification_main(main)
