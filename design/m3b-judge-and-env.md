# M3b: Environment configuration and historical judge agreement

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

This reference retains configuration and experiment boundaries from 2026-10-16. The gated/chat-judge configuration described historically was removed by P5; current flags are defined by the executable and [current contracts](current-contracts.md).

## Configuration precedence and secrecy

Precedence is CLI, then process environment, then a project-root dotenv file or explicit `--env-file`. A missing optional file is acceptable. Parse with `godotenv.Read`/`Parse`, not global `Load`/`Overload`. Support comments, `export`, quoting, and `${VAR}` expansion. Validate uppercase key names, detect duplicates before map construction, reject NUL and blocked keys, and warn on group/other-readable secret files.

Credentials, private endpoints, and personal paths must not appear in public reports, ledgers, cassettes, or prompt traces. Protocol facts such as models, modes, run seed, and decision rules remain disclosed. Evaluator `MEMEVAL_*` credentials are not SUT inputs. Any passthrough is an explicit allowlist and still rejects reserved evaluator/agent prefixes; checks expose variable names, never values. P8 supplies the current container gateway boundary.

## Historical judge experiment

Structured state separates `question`, `correct_answer` or `rubric`/`explanation`, and `model_response`; instructions preserve official decision wording while dropping text-output formatting demands. Noul confidence is `abs(2*p-1)`. An earlier gate escalated low-confidence cases to a chat judge and accounted for both calls separately. This gate, configurable thresholds, cross-model judge flags, and `judge-agreement` are retired, not current usage recommendations.

Agreement studies must reuse identical reader answers. Report confusion matrices, Cohen's kappa, Brier score, and ten-bin ECE with actual denominators. A seeded calibration/held-out split prevents fitting thresholds and evaluating them on the same samples; per-type samples below 20 were considered insufficient. Pairwise agreement between judges is not ground-truth accuracy. Current P5 uses binary cutoff 0.5 and argmax multivalued mapping without calibration flags.

Model availability metadata (`available`, `deprecated`, `retired`, `unknown`, shutdown date, source) is timestamped. Live policy and replay eligibility are separate. Model and effort changes affect cassette identity and reproducibility.

## Validation boundary

Parser/precedence, duplicate-key, secret exclusion, CLI coverage, and counted record/replay fixtures can be tested offline. Such tests do not validate real provider availability, real judge agreement, or an SUT run.
