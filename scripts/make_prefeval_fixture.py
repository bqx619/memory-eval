#!/usr/bin/env python3
"""Generate synthetic-only PrefEval conformance inputs; no upstream samples."""
import json
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1] / 'internal/importers/prefeval/testdata/input'
def write(path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj, ensure_ascii=False, indent=2) + '\n')
for topic in ['synthetic_colors','synthetic_shapes']:
    rows=[dict(preference=f'I prefer synthetic option {i}.',question=f'Which {topic} should I use in example {i}?',explanation='Private annotation.') for i in range(2)]
    write(ROOT/'explicit_preference'/f'{topic}.json', rows)
    choice=[];persona=[];mcq=[]
    for i,row in enumerate(rows):
        turns=dict(query='Choose an example?',assistant_options='One or two?',user_selection='One please.',assistant_acknowledgment='Understood.')
        pairs={'10':dict(user='Later preference.',assistant='Recorded.'),'2':dict(user='Earlier preference.',assistant='Noted.')}
        # Exercise both on-disk representations without authentic examples.
        choice.append(dict(row,conversation=repr(turns) if i else turns,aligned_op='private',implicit_query='private',options=['private']))
        persona.append(dict(row,conversation=repr(pairs) if i else pairs,persona='private persona'))
        options=['preferred','alternative','other','last']
        mcq.append(dict(row,classification_task_options=repr(options) if i else options))
    write(ROOT/'implicit_preference/choice-based'/f'{topic}.json',choice)
    write(ROOT/'implicit_preference/persona-driven'/f'{topic}.json',persona)
    write(ROOT/'mcq_options'/f'{topic}.json',mcq)
write(ROOT/'filtered_inter_turns.json',[dict(conversation_id=f'synthetic-{i}',conversation=[dict(role='user',content='Filler question.'),dict(role='assistant',content='Filler reply.')]) for i in range(2)])
