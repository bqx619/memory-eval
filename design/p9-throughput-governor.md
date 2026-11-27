# P9: Parallel execution, gateway governance, and resumable limits

<!-- documentation-status: reference -->
> Technical reference. Historical milestones are not current operational instructions. See [current evaluation contracts](current-contracts.md); source types, schemas, and contract tests define implemented behavior.

This design extends P8 with runtime-episode scheduling and a per-run HTTP governor. It preserves isolation, deterministic case order, and explicit accounting. Later revisions below supersede the initial concurrency value of four.

## Single-target governor

One `__container-governor` serves each gateway-mode run, connected to an egress bridge and each internal episode network under alias `llm-gateway`. None/open runs do not create it. It forwards only to the configured target, rejecting CONNECT and absolute URIs for another host. P8's `relay_single_target` enforcement remains, with `metered:true`. The status listener is separate and inaccessible to SUTs.

Concurrency precedence is explicit CLI/environment value, otherwise `(manifest llm.concurrency or default 8) * --parallel`. A manifest value describes per-episode fanout. Record effective concurrency, source, per-episode value, and parallelism. Requests-per-minute is a token bucket with default unlimited. Upstream timeout defaults to 180 seconds.

Retry 429/502/503/504 and upstream transport/read errors with Retry-After or jittered exponential backoff (one second initial, 60-second cap), at most six retries. Keep the downstream connection while waiting. Five consecutive retryable upstream failures or Retry-After above 60 seconds opens the circuit; after 60 seconds, one live request probes half-open. Failed probes double cooldown up to 600 seconds.

A canceled downstream request is `status:0,error:"client disconnected"`, with queue wait recorded. It increments `client_disconnects`, not retries or upstream circuit failures. A canceled half-open request relinquishes its slot without doubling cooldown. Queue wait p95 and disconnects explain client timeouts; increasing governor slots does not alter an SUT client's own deadline.

## Budgets, accounting, and interruption

`--sut-llm-budget-tokens` counts prompt plus completion tokens; zero is unlimited. Cumulative paused time is bounded by `--sut-llm-max-pause` (default 30 minutes). Exhaustion returns 503 with `X-Memeval-Governor: exhausted|budget`, stops new scheduling, preserves completed cases, and writes a resumable interruption with exit code 4. Pending cases must not silently become successful or disappear from planned coverage.

Non-streaming usage comes from JSON; streaming requests request `stream_options.include_usage` and parse final SSE usage while preserving streaming bytes. Missing usage remains null/unmetered, never an estimate presented as measured. Cached and reasoning tokens are subsets, not additive double charges.

Meter JSONL records `{t,ms,episode_ip,path,model,status,attempt,prompt_tokens,cached_tokens,completion_tokens,reasoning_tokens,retry_after}` plus queue/error diagnostics. Source IP maps to container and episode. Logs are private 0600 files; publication strips deployment addresses. `billing.sut` records calls/tokens/retries/rate-limited responses/circuit opens/client disconnects/queue p95 and per-episode aggregates. `isolation.container.governor` records effective configuration and circuit events. `GET /memeval/status` provides counters, circuit state, and suggested cooldown to the host.

The governor is labeled with run ownership and role, uses container security flags and a pids limit of 64, and mounts only its meter directory. Episode prepare connects it; destroy disconnects it; run cleanup destroys it and includes it in residue checks. Selftest creates an equivalent short-lived governor.

## Scheduling and resume

`--parallel` accepts 1 through 16, default 1. The unit is a runtime episode: with `fresh_per_case`, each case and its repeats form an independent task; with `incremental_per_episode`, the entire suite episode stays ordered in one worker. Each worker owns its adapter/container/directory. Shared accounting and warnings are synchronized. Results are assembled in suite order, independent of completion order, and a fixed run seed does not depend on scheduling.

Logs may interleave and use startup sequence numbers; this is not case ordering. CPU and memory limits are manifest configuration and must be disclosed. Parallelism beyond available resources can reduce throughput.

`--auto-resume-rate-limited N` defaults off and retries eligible `gateway_exhausted`, `gateway_budget`, or `rpc_timeout` cases after cooldown, up to N rounds. Each attempt remains in resume history. Budget increases or other resume configuration changes are recorded rather than hidden.

Isolation failures include a bounded sanitized first cause, not just a generic failure. Sidecar readiness errors say `readiness timeout after <timeout_s>s`; log tails remove ANSI escapes and credentials.

## Verification scope

Fake upstreams exercise throttling, connection failure, slow/canceled requests, SSE/JSON usage, concurrency, retry bounds, circuit transitions, and budget stopping. Fake Docker tests cover network connect/disconnect/destroy and residue. Deterministic baselines compare serial and parallel non-timing results; race checks cover shared state. Real throughput and fault-injection measurements are separate historical evidence, not asserted by these fixtures. Cross-run coordination and shared limiting with the evaluator's model client are outside this design.
