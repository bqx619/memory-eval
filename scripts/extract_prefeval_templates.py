#!/usr/bin/env python3
"""Extract pinned PrefEval literals without importing upstream clients."""
import ast
import json
from pathlib import Path
import sys
from upstream import require_upstream
ROOT = Path(__file__).resolve().parents[1]

def literals():
    src = require_upstream('prefeval', ROOT)
    for path in ('error_type/check_acknowledge.txt', 'error_type/check_violation.txt',
                 'error_type/check_helpful.txt', 'error_type/check_hallucination.txt',
                 'generation_task/llm_based_evaluation_errortypes.py',
                 'utils/common_utils.py', 'utils/utils_mcq.py', 'config.yaml',
                 'utils/explicit_utils.py'):
        require_upstream('prefeval/' + path, ROOT)
    def tree(path): return ast.parse((src / path).read_text())
    def assignment(t, key):
        return next(n.value for n in ast.walk(t) if isinstance(n, ast.Assign)
                    and any(isinstance(x, ast.Name) and x.id == key for x in n.targets))
    def text(n):
        if isinstance(n, ast.Constant): return n.value
        if isinstance(n, ast.JoinedStr):
            return ''.join(v.value if isinstance(v, ast.Constant) else '{' + ast.unparse(v.value) + '}' for v in n.values)
        raise ValueError(ast.dump(n))
    out = {name: (src / ('error_type/check_' + file + '.txt')).read_text()
           for name, file in [('acknow','acknowledge'),('violate','violation'),('helpful','helpful'),('hallucinate','hallucination')]}
    out['judge_system'] = text(assignment(tree('generation_task/llm_based_evaluation_errortypes.py'), 'system_prompt'))
    out['reminder'] = text(assignment(tree('utils/common_utils.py'), 'REMINDER'))
    out['mcq_question'] = text(assignment(tree('utils/utils_mcq.py'), 'mcq_question_format'))
    cfg = (src / 'config.yaml').read_text()
    out['reader_system'] = ast.literal_eval(next(s.split(':',1)[1].strip() for s in cfg.splitlines() if s.startswith('system_prompt:')))
    # The same format string occurs in both explicit and implicit Claude branches.
    exprs = [text(n) for n in ast.walk(tree('utils/explicit_utils.py')) if isinstance(n, ast.JoinedStr)]
    suffix = next(s for s in exprs if '(Please respond within {max_tokens} words.)' in s)
    out['generation_suffix'] = suffix[suffix.index(' (Please respond'):]
    assert out['generation_suffix'] == ' (Please respond within {max_tokens} words.)'
    return out

if __name__ == '__main__':
    target = ROOT / 'internal/scoring/prefeval/templates.json'
    data = (json.dumps(literals(), ensure_ascii=False, indent=2) + '\n').encode()
    if '--check' in sys.argv: assert target.read_bytes() == data, 'PrefEval template drift'
    else: target.write_bytes(data)
    print('PrefEval four probes, system, reminder and reader literals exact')
