# Source and documentation publication checklist

Project code uses [MIT](../LICENSE); third-party materials retain their own terms. A release should identify one source snapshot and ship matching documentation, schemas, viewer resources, and verification evidence.

## Public entry points

Keep the root guides, license and notices, generic collaboration rules, data/protocol/adapter guides, current contracts, design index, and viewer source. Each SUT has equal status. A specialized trace viewer is an optional format extension, not the framework's evaluation target.

Ship each page with its JS/CSS dependencies. Generated `docs-site/{beam,cases,results}/` data require separate review. Do not include local multi-gigabyte traces by default.

## Historical records and private material

Historical paths remain discoverable through the [history index](history.md). Long execution transcripts have been condensed into English publication records that preserve purpose, outcomes, and limitations, with the original document hash. Private deployment, coordination, and temporary-workspace details are excluded. These summaries are not a claim that the original transcripts are public-safe or that old tests were rerun.

The working tree and Git history are different publication surfaces. Editing current files or adding ignore rules does not remove previously committed information. No history rewriting or publication is performed by this cleanup. A full-history release needs a separate history-sensitive-content review; a selected source release should state its provenance.

## Inputs that must remain intact

| Material | Why it matters |
|---|---|
| `docs/data/pmb_projection.json` | Default built-in PMB input |
| Prototype JSON/JSONL under `docs/reports/` | Regression references |
| `docs/ARTIFACT_MANIFEST.json` | Original artifact provenance plus explicit documentation-revision hashes |
| P8 image appendix | Exact image reference/digest agreement tests |
| `design/report-ui/` and `internal/report/assets/` | Byte-synchronized report templates |
| Pinned `SOURCES.md` and original licenses | Provenance, restoration, and attribution |

English revisions to prototype prose or display text are documented as revisions; they must not silently replace original artifact hashes. Dataset and metric values stay unchanged.

## Before publishing

1. Select the source snapshot and include required viewer resources and generators together.
2. Preserve MIT, third-party notices, and fixture licenses; audit the actual distribution contents.
3. Verify offline baselines, schemas, relative links, HTML entry points, and missing-data behavior in a clean checkout. Report optional tests that were not run.
4. Select artifacts explicitly. Check licensing, credentials, source/prompt text, coverage, and resumed identities before publishing any generated data.
5. Review both release contents and history. A sanitized working tree is not a guarantee that historical commits or private artifacts are clean.
