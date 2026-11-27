#!/usr/bin/env python3
"""Extract pinned Memora literals with AST; never import upstream clients."""
import ast,json,pathlib,sys
from upstream import require_upstream
ROOT=pathlib.Path(__file__).resolve().parents[1]
def literals():
 root=require_upstream('memora/evals', ROOT)
 for path in ('agent_eval/memory_to_answer.py', 'model_eval/model_based_evaluator.py', 'model_eval/config.py'):
  require_upstream('memora/evals/' + path, ROOT)
 agent=ast.parse((root/'agent_eval/memory_to_answer.py').read_text())
 model=ast.parse((root/'model_eval/model_based_evaluator.py').read_text())
 def method(tree,name):return next(n for n in ast.walk(tree) if isinstance(n,ast.FunctionDef) and n.name==name)
 def text(n):
  if isinstance(n,ast.Constant):return n.value
  if isinstance(n,ast.JoinedStr):return ''.join(v.value if isinstance(v,ast.Constant) else '{'+ast.unparse(v.value)+'}' for v in n.values)
  raise ValueError(ast.dump(n))
 def assignment(fn,key):return next(n.value for n in ast.walk(fn) if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id==key for t in n.targets))
 out={}
 for prefix,fn in [('judge',method(agent,'_evaluate_with_single_judge')),('sources',method(agent,'generate_answer')),('full',method(model,'build_question_prompt'))]:
  for k in ['system_prompt','user_prompt']:out[prefix+'_'+k]=text(assignment(fn,k))
 out['full_date_context']=text(next(n for n in ast.walk(method(model,'build_question_prompt')) if isinstance(n,ast.JoinedStr) and 'IMPORTANT:' in text(n)))
 out['full_header']=text(assignment(method(model,'build_conversation_context'),'header'))
 config=ast.parse((root/'model_eval/config.py').read_text())
 limits=next(ast.literal_eval(n.value) for n in config.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='MODEL_CONTEXT_LIMITS' for t in n.targets))
 return out,list(limits.items())
if __name__=='__main__':
 templates,limits=literals()
 for name,obj in [('templates.json',templates),('context_limits.json',limits)]:
  b=(json.dumps(obj,ensure_ascii=False,indent=2)+'\n').encode();p=ROOT/'internal/scoring/memora'/name
  if '--check' in sys.argv:assert p.read_bytes()==b,name+' drift'
  else:p.write_bytes(b)
 print('Memora 8 verbatim literals and ordered model context declarations exact')
