#!/usr/bin/env python3
"""Independent Merkle recomputation; safe from any working directory."""
import sys
sys.dont_write_bytecode = True
from verification import verification_main
import hashlib, json, pathlib
class Number(str): pass
def canonical(v):
    if isinstance(v, Number): return v
    if isinstance(v, dict): return '{' + ','.join(canonical(k)+':'+canonical(v[k]) for k in sorted(v)) + '}'
    if isinstance(v, list): return '[' + ','.join(map(canonical,v)) + ']'
    return json.dumps(v,ensure_ascii=False,separators=(',',':')).replace('\u2028','\\u2028').replace('\u2029','\\u2029')
def main(args, work):
    p=args.bundle; h=json.loads((p/'suite.json').read_bytes(),parse_int=Number,parse_float=Number); index=(p/'episodes.index.jsonl').read_bytes()
    assert hashlib.sha256(index).hexdigest()==h['container']['index_sha256']
    root=hashlib.sha256(canonical(h).encode()+b'\n'+index).hexdigest()
    if args.expected: assert root==args.expected, (root,args.expected)
    print(root)
    (work.out/'merkle.json').write_text(json.dumps({'bundle':str(p),'sha256':root})+'\n')

if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument('bundle', type=pathlib.Path)
    parser.add_argument('expected', nargs='?')
    verification_main(main, parser)
