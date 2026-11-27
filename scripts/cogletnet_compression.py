#!/usr/bin/env python3
"""Per-layer compression ratio (压缩率) of a CogletNet run's summary trees.

Usage: python3 -B scripts/cogletnet_compression.py <report-dir>... [--json OUT] [--per-episode]

压缩率(Lk) = Σtokens(Lk) / Σtokens(Lk-1), summed over the final snapshot of every
episode. Token counts are the build tokenizer counts recorded by CogletNet itself:
each accepted summary record in the `sut_trace {"kind":"build",...}` lines of
logs/*/sut.stderr.log (COGLETNET_WORK_TRACE=full) carries `input_tokens` =
count(body.text) of the summarised body. These exact input counts take priority.
A highest-layer root may never be summarised itself: when its body has no input
count, use the sum of output_tokens from the accepted summaries for every body
in member.inputs. Inputs must be nonempty and all accepted output counts must
be present; incomplete evidence remains missing, not a partial/zero estimate.
This fallback sums the constituent summaries, so tokenizer boundary effects at
concatenation may differ slightly from tokenising the whole node body. Disclose
the number of fallback members per level as "tokens from input summaries" and
JSON tokens_from_input_summaries; omit this field/line when unused to preserve
existing output bytes.
For each episode the latest attempt (logs/0*-paeval_* or logs/resume-N/0*-paeval_*)
that holds build traces is used; episodes are identified by their first event id.
Also reports the per-candidate-kind ratio (ExactCopy / ExtractivePrefix / Model),
the accepted summaries flagged `length_overflow` / `degraded` (abstractive-ratio-v2 soft acceptance),
how many attempts accepted summaries needed, and the summary configuration seen. Read-only; stdlib only.
"""
import glob
import json
import os
import statistics
import sys
from collections import Counter, defaultdict


def attempts(run):
    out = []
    for d in glob.glob(os.path.join(run, 'logs', '0*-paeval_*')):
        out.append((0, int(os.path.basename(d)[:4]), d))
    for rd in glob.glob(os.path.join(run, 'logs', 'resume-*')):
        try:
            n = int(rd.rsplit('-', 1)[1])
        except ValueError:
            continue
        for d in glob.glob(os.path.join(rd, '0*-paeval_*')):
            out.append((n, int(os.path.basename(d)[:4]), d))
    return sorted(out)


def builds(attempt_dir):
    path = os.path.join(attempt_dir, 'sut.stderr.log')
    if not os.path.exists(path):
        return []
    found = []
    with open(path, encoding='utf-8', errors='replace') as handle:
        for line in handle:
            tag, _, rest = line.partition(' ')
            if tag != 'sut_trace' or '"build"' not in rest[:64]:
                continue
            record = json.loads(rest)
            if isinstance(record, dict) and record.get('kind') == 'build':
                found.append(record)
    return found


def latest_attempts(run):
    """episode key (first build event_id) -> (resume_index, seq, dir, builds); later attempts win."""
    chosen = {}
    for ridx, seq, d in attempts(run):
        found = builds(d)
        if found:
            chosen[found[0].get('event_id')] = (ridx, seq, d, found)
    return chosen


def episode_layers(found):
    """Return (tokens, members, missing, records, config, rejected, tokens_from_input_summaries)."""
    tokens = {}
    records = {}
    rejected = Counter()
    config = None
    final = None
    for build in found:
        audit = build.get('audit') or {}
        cfg = (audit.get('operation') or {}).get('config') or {}
        if cfg:
            config = cfg
        for rec in audit.get('summaries') or []:
            if rec.get('accepted'):
                if rec.get('input_tokens') is not None:
                    tokens[rec['body']] = rec['input_tokens']
                records[rec['body']] = rec
            else:
                rejected[rec.get('candidate_kind')] += 1
        snapshot = build.get('snapshot') or {}
        if snapshot.get('members'):
            final = snapshot
    level_tokens, level_members, missing = Counter(), Counter(), Counter()
    from_summaries = Counter()
    body_level = {}
    for member in (final or {}).get('members') or []:
        body = member['body']['reference']['body']
        level = member['level']
        body_level[body] = level
        level_members[level] += 1
        if body in tokens:
            level_tokens[level] += tokens[body]
        else:
            inputs = [records.get(ref.get('body')) if isinstance(ref, dict) else None
                      for ref in member.get('inputs') or []]
            if inputs and all(rec is not None and type(rec.get('output_tokens')) is int
                              and rec['output_tokens'] >= 0 for rec in inputs):
                level_tokens[level] += sum(rec['output_tokens'] for rec in inputs)
                from_summaries[level] += 1
            else:
                missing[level] += 1
    for body, rec in records.items():
        rec['_level'] = body_level.get(body)
    return level_tokens, level_members, missing, records, config, rejected, from_summaries


def quantile(values, p):
    values = sorted(values)
    if not values:
        return float('nan')
    k = (len(values) - 1) * p
    lo, hi = int(k // 1), int(-(-k // 1))
    return values[lo] if lo == hi else values[lo] + (values[hi] - values[lo]) * (k - lo)


def analyse(run):
    chosen = latest_attempts(run)
    totals, members, missing, rejected = Counter(), Counter(), Counter(), Counter()
    from_summaries = Counter()
    ratios = defaultdict(list)
    kinds = defaultdict(list)
    configs = Counter()
    episodes = []
    for key, (ridx, seq, d, found) in chosen.items():
        lt, lm, miss, records, cfg, rej, fallback = episode_layers(found)
        totals.update(lt)
        members.update(lm)
        missing.update(miss)
        rejected.update(rej)
        from_summaries.update(fallback)
        if cfg:
            configs[json.dumps({k: cfg.get(k) for k in ('summary_ratio', 'summary_max', 'summary_policy', 'summary_rewrites', 'build_text_limits', 'chunk_target', 'chunk_low', 'chunk_high')}, sort_keys=True)] += 1
        for level in sorted(lt):
            if level >= 1 and lt.get(level - 1):
                ratios[level].append(lt[level] / lt[level - 1])
        for rec in records.values():
            if rec.get('input_tokens') and rec.get('output_tokens') is not None:
                kinds[rec.get('candidate_kind')].append((rec['input_tokens'], rec['output_tokens']))
        episodes.append({'attempt': os.path.relpath(d, run), 'resume': ridx, 'tokens': {f'L{l}': lt[l] for l in sorted(lt)},
                         'ratios': {f'L{l}/L{l-1}': round(lt[l] / lt[l - 1], 4) for l in sorted(lt) if l >= 1 and lt.get(l - 1)}})
    layers = []
    previous = None
    for level in sorted(totals):
        row = {'level': level, 'members': members[level], 'tokens': totals[level],
               'ratio': (totals[level] / previous) if previous else None,
               'vs_l0': totals[level] / totals[0] if totals.get(0) else None,
               'episode_median': statistics.median(ratios[level]) if ratios.get(level) else None,
               'episode_min': min(ratios[level]) if ratios.get(level) else None,
               'episode_max': max(ratios[level]) if ratios.get(level) else None}
        layers.append(row)
        previous = totals[level]
    upper = sum(v for l, v in totals.items() if l >= 1)
    outcomes = Counter()
    attempts = Counter()
    for key, (ridx, seq, d, found) in chosen.items():
        for rec in episode_layers(found)[3].values():
            if rec.get('length_overflow'):
                outcomes['length_overflow'] += 1
            degraded = rec.get('degraded')
            if degraded:
                outcomes['degraded:' + str(degraded)] += 1
            if rec.get('ordinal') is not None:
                attempts[int(rec['ordinal']) + 1] += 1
    kind_rows = []
    for kind, pairs in sorted(kinds.items(), key=lambda kv: -len(kv[1])):
        s_in = sum(p[0] for p in pairs)
        s_out = sum(p[1] for p in pairs)
        kind_rows.append({'kind': kind, 'n': len(pairs), 'input_tokens': s_in, 'output_tokens': s_out,
                          'ratio': s_out / s_in if s_in else None, 'median': statistics.median(o / i for i, o in pairs)})
    result = {'run': run, 'episodes': len(chosen), 'attempts_root': sum(1 for v in chosen.values() if v[0] == 0),
            'attempts_resume': sum(1 for v in chosen.values() if v[0] > 0), 'missing_token_counts': dict(missing),
            'config': [json.loads(k) for k in configs], 'layers': layers,
            'upper_vs_l0': upper / totals[0] if totals.get(0) else None, 'kinds': kind_rows,
            'rejected_attempts': dict(rejected), 'outcomes': dict(outcomes), 'attempts': {str(k): v for k, v in sorted(attempts.items())},
            'per_episode': episodes}
    if from_summaries:
        result['tokens_from_input_summaries'] = dict(sorted(from_summaries.items()))
    return result


def fmt(value, digits=4):
    return '-' if value is None else f'{value:.{digits}f}'


def print_report(result, per_episode=False):
    print(f"== {result['run']}  episodes {result['episodes']} (attempts: root {result['attempts_root']}, resume {result['attempts_resume']}); "
          f"members without token count: {result['missing_token_counts'] or 0}")
    if result.get('tokens_from_input_summaries'):
        print(f"   tokens from input summaries: {result['tokens_from_input_summaries']}")
    for cfg in result['config']:
        print(f"   config: summary_ratio {cfg.get('summary_ratio')} summary_max {cfg.get('summary_max')} policy {cfg.get('summary_policy')} "
              f"rewrites {cfg.get('summary_rewrites')} limits {cfg.get('build_text_limits')} chunk {cfg.get('chunk_low')}/{cfg.get('chunk_target')}/{cfg.get('chunk_high')}")
    print(f"   {'layer':<6}{'members':>8}{'tokens':>11}{'压缩率 Lk/Lk-1':>16}{'vs L0':>9}{'ep median':>11}{'ep min':>9}{'ep max':>9}")
    for row in result['layers']:
        print(f"   L{row['level']:<5}{row['members']:>8}{row['tokens']:>11}{fmt(row['ratio']):>16}{fmt(row['vs_l0']):>9}"
              f"{fmt(row['episode_median'], 3):>11}{fmt(row['episode_min'], 3):>9}{fmt(row['episode_max'], 3):>9}")
    print(f"   upper layers / L0: {fmt(result['upper_vs_l0'])}")
    print(f"   {'kind':<18}{'n':>7}{'Σin':>10}{'Σout':>10}{'Σout/Σin':>10}{'median':>8}")
    for row in result['kinds']:
        print(f"   {str(row['kind']):<18}{row['n']:>7}{row['input_tokens']:>10}{row['output_tokens']:>10}{fmt(row['ratio'], 3):>10}{fmt(row['median'], 3):>8}")
    if result['rejected_attempts']:
        print(f"   rejected candidate attempts: {result['rejected_attempts']}")
    if result.get('outcomes'):
        print(f"   accepted with length_overflow / degraded: {result['outcomes']}")
    if result.get('attempts'):
        print(f"   accepted after N attempts: {result['attempts']}")
    if per_episode:
        for ep in sorted(result['per_episode'], key=lambda e: (e['resume'], e['attempt'])):
            print(f"   {ep['attempt']:<48} {ep['tokens']} {ep['ratios']}")


def main(argv):
    out = None
    per_episode = False
    runs = []
    args = iter(argv)
    for arg in args:
        if arg == '--json':
            out = next(args)
        elif arg == '--per-episode':
            per_episode = True
        else:
            runs.append(arg)
    if not runs:
        print(__doc__)
        return 2
    results = [analyse(run) for run in runs]
    for result in results:
        print_report(result, per_episode)
    if out:
        with open(out, 'w', encoding='utf-8') as handle:
            json.dump(results, handle, ensure_ascii=False, indent=1)
        print(f"wrote {out}")
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
