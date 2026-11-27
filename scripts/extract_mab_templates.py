"""Extract only pinned literal assignments; never import upstream side effects."""
import argparse
import ast
import json
from pathlib import Path
from upstream import require_upstream

ROOT = Path(__file__).resolve().parents[1]


def extract():
    root = require_upstream("memoryagentbench", ROOT)
    for path in ("utils/templates.py", "llm_based_eval/summarization_evaluate.py"):
        require_upstream("memoryagentbench/" + path, ROOT)
    nodes = ast.parse((root / "utils/templates.py").read_text()).body
    selected = [n for n in nodes if isinstance(n, ast.Assign) and any(
        isinstance(t, ast.Name) and t.id in ("SYSTEM_MESSAGE", "BASE_TEMPLATES") for t in n.targets)]
    ns = {}
    exec(compile(ast.Module(selected, type_ignores=[]), "literal_templates", "exec"), {}, ns)
    nodes = ast.parse((root / "llm_based_eval/summarization_evaluate.py").read_text()).body
    summary = {n.targets[0].id: ast.literal_eval(n.value) for n in nodes
               if isinstance(n, ast.Assign) and isinstance(n.targets[0], ast.Name)
               and n.targets[0].id in ("fluency_prompt_book", "recall_prompt_book", "precision_prompt_book")}
    return {"templates.json": ns, "summary.json": summary}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    for name, value in extract().items():
        path = ROOT / "internal/benchmark/mab" / name
        if args.check:
            assert json.loads(path.read_text()) == value, name
        else:
            path.write_text(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
        print(name + ": literal parity OK")
