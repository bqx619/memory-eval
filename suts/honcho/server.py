"""Launch the reviewed server argv; no guessed entry point is executed."""
import json
import os
import sys
from pathlib import Path
contract = json.loads(Path(__file__).with_name('server-contract.json').read_text())
argv = contract.get('argv')
if not argv:
    raise SystemExit('Server entry point/Unix pg0 binding not verified; populate server-contract.json after source audit')
# Shell-free argv expansion; all paths and ports were allocated by the runner.
values = {'python': sys.executable, 'ep': os.environ['EP']}
for i in range(1, len(sys.argv)-1, 2):
    values[sys.argv[i].lstrip('-')] = sys.argv[i+1]
argv = [part.format(**values) for part in argv]
os.execv(argv[0], argv)
