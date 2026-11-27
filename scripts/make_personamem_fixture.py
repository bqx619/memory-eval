#!/usr/bin/env python3
"""Reproduce the two-question MIT fixture; never modifies upstream inputs."""
import csv
import json
from pathlib import Path
from upstream import require_upstream
root = Path(__file__).resolve().parents[1]
license_text = require_upstream('personamem/LICENSE', root).read_text()
source = root / 'data/upstream/personamem-v1'
target = root / 'internal/importers/personamem/testdata'
with (source / 'questions_32k.csv').open(newline='') as f:
    reader = csv.DictReader(f)
    fields = reader.fieldnames
    rows = list(reader)
cid = rows[0]['shared_context_id']
selected = [r for r in rows if r['shared_context_id'] == cid][:2]
end = max(int(r['end_index_in_shared_context']) for r in selected)
context = next(json.loads(line)[cid] for line in (source / 'shared_contexts_32k.jsonl').open() if cid in json.loads(line))
target.mkdir(parents=True, exist_ok=True)
with (target / 'questions.csv').open('w', newline='') as f:
    w = csv.DictWriter(f, fieldnames=fields)
    w.writeheader()
    w.writerows(selected)
(target / 'contexts.jsonl').write_text(json.dumps({cid: context[:end]}, ensure_ascii=False) + '\n')
(target / 'LICENSE.txt').write_text(license_text)
