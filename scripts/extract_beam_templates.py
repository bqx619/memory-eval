#!/usr/bin/env python3
"""Extract literals through AST only; never import upstream modules (they download models)."""
import ast,json,pathlib,sys
from upstream import require_upstream
ROOT=pathlib.Path(__file__).resolve().parents[1]
def literals():
 p=require_upstream('beam/src', ROOT)
 for path in ('prompts.py', 'evaluation/compute_metrics.py', 'answer_probing_questions/long_term_memory_methods.py'):
  require_upstream('beam/src/' + path, ROOT)
 tree=ast.parse((p/'prompts.py').read_text())
 rubric=next(ast.literal_eval(n.value) for n in tree.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='unified_llm_judge_base_prompt' for t in n.targets))
 tree=ast.parse((p/'evaluation/compute_metrics.py').read_text())
 fn=next(n for n in tree.body if isinstance(n,ast.FunctionDef) and n.name=='llm_equivalence')
 def format_text(node):
  if isinstance(node,ast.Constant):return node.value
  return ''.join(x.value if isinstance(x,ast.Constant) else '{'+x.value.id+'}' for x in node.values)
 messages={n.targets[0].id:next(v for k,v in zip(n.value.keys,n.value.values) if ast.literal_eval(k)=='content') for n in fn.body if isinstance(n,ast.Assign) and isinstance(n.value,ast.Dict)}
 tree=ast.parse((p/'answer_probing_questions/long_term_memory_methods.py').read_text())
 note=next(format_text(n) for n in ast.walk(tree) if isinstance(n,ast.JoinedStr) and 'NOTE: Only provide the answer' in format_text(n))
 return {'rubric':rubric,'equivalence_system':format_text(messages['system_msg']),'equivalence_user':format_text(messages['user_msg']),'reader_note':note}
if __name__=='__main__':
 data=(json.dumps(literals(),ensure_ascii=False,indent=2)+'\n').encode();dest=ROOT/'internal/scoring/judge/beam/templates.json'
 if '--check' in sys.argv:
  assert dest.read_bytes()==data,'template literal drift';print('BEAM four literal templates exact')
 else:dest.write_bytes(data)
