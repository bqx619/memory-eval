# P12: Retrieved-text input and provenance contract

<!-- documentation-status: reference -->
> Technical reference. Historical milestones and examples are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define the implemented format.

The fixed-reader `items` mode evaluates the text actually returned by an SUT, including rewritten memories with no event citation. It must not reconstruct a better answer input from original events. This extension applies only to readers that explicitly implement it; unsupported reader/mode combinations fail before execution.

## Reader and provenance rules

- `sources` remains the default original-event projection. `items` preserves actual returned text and rank. Gold, future events, and private labels never enter either reader input.
- Character accounting uses Unicode code points, including separators. Truncation removes complete lowest-ranked trailing items, records the truncation, and limits fidelity to partial.
- Empty `source_ids` is allowed only for supported `fixed_reader` plus `items`. Every nonempty citation must resolve to an ingested public event; unknown, future, and duplicate citations remain errors. Structural, budget, and explicit unsupported errors are not waived.
- Bridges preserve invalid nonempty references rather than converting them to empty arrays. An absent upstream document ID may produce an empty list while retaining text. The bridge does not choose reader mode.
- A mixed-provenance case can receive an answer score, but retrieval metrics are unavailable for the entire case. A traceable subset must not masquerade as complete retrieval scoring. Record untraceable-item counts and reasons. Fully traceable behavior is unchanged.
- `fullcontext` with `items` is rejected before execution. Report, resume, comparison, and cassette identity distinguish reader modes.

This contract does not change an upstream SUT's retrieval algorithm or native-answer behavior.

## Validation

Offline fake-SUT to runner to fake-reader tests assert that returned rewritten text reaches the reader and replaced original text does not. Counterexamples cover invalid citations, future/gold leakage, Unicode budgets, bridge behavior, and LongMemEval consistency. Passing fixtures is not evidence of a real container or provider run.
