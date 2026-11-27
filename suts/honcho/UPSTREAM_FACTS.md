# honcho adapter facts

<!-- current-adapter-entry -->
## Current adapter entry

This SUT follows the same [integration contract](../README.md) and [evaluation boundaries](../../design/current-contracts.md) as every other SUT. The table describes current manifest declarations, not completed acceptance for every combination. Check the actual `describe` output and version-specific evidence for SDK configuration, capabilities and limitations.

| Manifest | Name | Variant | Runtime network | Declared status |
|---|---|---|---|---|
| [manifest.json](manifest.json) | honcho | local | open | deferred |

Check the selected manifest for models, embeddings and runtime limits; absence of a declaration does not establish support. Adapter-specific build or retrieval traces are diagnostic outputs, not requirements imposed on other SUTs.

## Versioned verification history

The records below preserve dated source checks, migrations and corrections. Host-era installation and isolation details are historical; external SUTs now run only in Linux containers. Later corrections and the actual implementation take precedence for the corresponding variant. Historical commands are not current installation instructions.

## Honcho 3.3.0: deferred integration (P6b)

The P6b source survey identified Python ≥3.13 and AGPL-3.0 for the service. At the initial design stage, no real source/wheel was supplied to verify constructors, Unix HTTP transport, service entry points or DTOs.

- Known named APIs were `session.add_messages([peer.message(content,created_at=)])`, `session.search(query,limit)`, queue_status and DERIVER_FLUSH_ENABLED. Fixtures preserve timezone-aware datetimes. Returned message lists and queue_status.pending were explicit unverified adapter assumptions, not established upstream facts.
- client_factory must come from pinned project code; server-contract.json is also unverified. session(ns)/peer(ns) are project hooks that a wrapper must map to the real API. Reset isolates a new physical session; episode cleanup removes the whole database. No per-record deletion claim is made.
- The historical database preset used boomship v0.3.1, PostgreSQL 18.6 and pgvector 0.8.6, with three-platform archive hashes from Appendix A. It verified archives/lib/vector and confined database connections to episode sockets. Schema/migrations/vector extension creation depended on a verified service entry point.
- Search IDs must map to ingested message IDs; unknown IDs are unsupported. No complete enumeration API was verified, so dump is not declared. Unknown/noninteger pending counts reject readiness rather than assuming an empty queue.
- Service version 3.3.0 is not a Python client distribution version; no `honcho-ai==3.3.0` was invented. requirements.in/lock remain explicit placeholders and installation rejects them. Fixture coverage of counts, timestamps, queue drain, provenance and reset isolation does not prove real server/PostgreSQL acceptance.

### Deferred decision (P6b-2, 2026-10-18)

`design/research/p6b-project-facts-2026-10.md` records that the service has no PyPI distribution and requires source plus uv sync (Python 3.13). A separate deriver process, metrics port 9090, direct embedding/LLM traffic and asynchronous deletion were outside the batch. status=deferred makes env install fail with the P6c reason before downloads or subprocesses. The skeleton is excluded from P6b-2 acceptance.

### Container skeleton (P8 K4, 2026-10-19)

V3 declares `postgres-pgvector-18`, episode-network alias `postgres:5432`, vector extension initialization after readiness, and a generic API at `0.0.0.0:8888` with `/health`, without host-published ports. These are generic container addresses, not a deployment endpoint.

**The adapter remains deferred and installation still rejects it.** Real server.py/server-contract entry points, deriver lifecycle and readiness require design and actual verification. This is an orchestration skeleton, not a verified Honcho startup contract.
