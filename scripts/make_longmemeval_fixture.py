#!/usr/bin/env python3
"""Select three unmodified MIT LongMemEval questions, retaining answer sessions
and two filler sessions per question. Only the three parallel haystack arrays
are subsetted; question fields, session fields and source text remain original.
"""
import argparse
import json
from pathlib import Path

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, default=Path('data/upstream/longmemeval-s/longmemeval_s_cleaned.json'))
    parser.add_argument('--out', type=Path, default=Path('internal/importers/longmemeval/testdata/fixture.json'))
    args = parser.parse_args()
    rows = json.loads(args.input.read_text())
    normal = next(row for row in rows if '_abs' not in row['question_id'])
    abstain = next(row for row in rows if '_abs' in row['question_id'])
    other = next(row for row in rows if '_abs' not in row['question_id'] and row['question_type'] != normal['question_type'])
    selected = []
    for row in (normal, abstain, other):
        answer_ids = set(row['answer_session_ids'])
        for sid, session in zip(row['haystack_session_ids'], row['haystack_sessions']):
            if any(turn.get('has_answer') for turn in session):
                answer_ids.add(sid)
        fillers = [i for i, sid in enumerate(row['haystack_session_ids']) if sid not in answer_ids][:2]
        keep = sorted(set(fillers) | {i for i, sid in enumerate(row['haystack_session_ids']) if sid in answer_ids})
        trimmed = dict(row)
        for field in ('haystack_session_ids', 'haystack_dates', 'haystack_sessions'):
            trimmed[field] = [row[field][i] for i in keep]
        selected.append(trimmed)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(selected, ensure_ascii=False, indent=2) + '\n')
    print(f'{len(selected)} questions, {sum(len(r["haystack_sessions"]) for r in selected)} session occurrences -> {args.out}')

if __name__ == '__main__':
    main()
