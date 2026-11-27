//! External host: durable ingestion, bounded builds, strict native retrieval.
use adapters::environment::LiveEnvironment;
use cogletnet::*;
use serde_json::{Value, json};
use std::io::{self, BufRead, Write};
use std::time::Duration;
use std::{future::Future, pin::Pin, sync::Arc};
use tokio::time::Instant;

const MAX_FRAME: usize = 16 * 1024 * 1024;
const DEFAULT_BATCH_SECONDS: u64 = 1740;

// Implement async_trait's public boxed-future ABI without adding a dependency.
// The host owns the queue; the library's default environment has no scheduler.
struct BuildQueue(tokio::sync::mpsc::UnboundedSender<OperationId>);
impl HostScheduler for BuildQueue {
    fn schedule<'s, 'n, 'a>(&'s self, _namespace: &'n str, operation: OperationId)
        -> Pin<Box<dyn Future<Output = Result<()>> + Send + 'a>>
        where 's: 'a, 'n: 'a, Self: 'a,
    {
        Box::pin(async move {
            self.0.send(operation).map_err(|_| Error::new(ErrorKind::Closed, "host build queue closed"))
        })
    }
}

async fn wait_scoped(memory: &CogletNet, operation: OperationId, rows: &[Value], deadline: Instant) -> Result<Value> {
    let mut status = None;
    let mut pause = Duration::from_secs(1);
    while Instant::now() < deadline {
        let observed = match tokio::time::timeout_at(deadline, memory.operation_status(operation)).await {
            Ok(value) => value.map_err(|e| e.operation(operation))?,
            Err(_) => break,
        };
        let terminal = matches!(observed.state, OperationState::Ready | OperationState::Failed |
            OperationState::Cancelled | OperationState::Superseded | OperationState::Unknown);
        status = Some(observed);
        if terminal { break; }
        tokio::time::sleep_until(std::cmp::min(Instant::now() + pause, deadline)).await;
        pause = std::cmp::min(pause * 2, Duration::from_secs(5));
    }
    let mut results = Vec::new();
    for row in rows {
        let id = string(row, "id")?;
        let result = if let Some(op) = &status {
            if op.state == OperationState::Ready {
                let scope: ScopeKey = serde_json::from_value(row["scope"].clone())?;
                // Adoption already persisted every Source before scheduling;
                // event_source performs no model call.
                let source = memory.event_source(scope, id).await.map_err(|e| e.operation(operation))?;
                let write = MemoryWriteResult::try_from(op.clone())?;
                json!({"event_id":id,"state":"Ready","operation_id":operation,"source_ref":source,"write":write})
            } else if let Some(error) = &op.error {
                failure(id, None, error.clone().operation(operation))
            } else if matches!(op.state, OperationState::Cancelled | OperationState::Superseded) {
                failure(id, None, Error::new(ErrorKind::OperationCancelled,
                    "scope operation cancelled or superseded before readiness").operation(operation))
            } else {
                json!({"event_id":id,"state":"Pending","operation_id":operation,"source_ref":null,
                       "write":MemoryWriteResult::try_from(op.clone())?})
            }
        } else {
            json!({"event_id":id,"state":"Pending","operation_id":operation,"source_ref":null})
        };
        results.push(result);
    }
    Ok(json!({"all_ready":results.iter().all(|r|r["state"]=="Ready"),"events":results}))
}
fn invalid(message: &str) -> Error {
    Error::new(ErrorKind::InvalidArgument, message)
}
fn string<'a>(v: &'a Value, key: &str) -> Result<&'a str> {
    v[key]
        .as_str()
        .ok_or_else(|| invalid("required string missing"))
}
fn window(request: &Value) -> Result<Duration> {
    let seconds = request
        .get("timeout_seconds")
        .map(|v| v.as_u64().ok_or_else(|| invalid("invalid timeout")))
        .transpose()?
        .unwrap_or(DEFAULT_BATCH_SECONDS);
    if !(1..=DEFAULT_BATCH_SECONDS).contains(&seconds) {
        return Err(invalid("timeout exceeds native execution window"));
    }
    Ok(Duration::from_secs(seconds))
}
fn failure(event_id: &str, source: Option<SourceRef>, error: Error) -> Value {
    let state = if matches!(
        error.kind,
        ErrorKind::OperationTimedOut | ErrorKind::OperationOutcomeUnknown
    ) {
        "Unknown"
    } else {
        "Failed"
    };
    json!({"event_id":event_id,"state":state,"operation_id":error.operation,"source_ref":source,"error":error})
}

#[tokio::main]
async fn main() {
    let mut environment: Option<LiveEnvironment> = None;
    let stdin = io::stdin();
    let mut input = stdin.lock();
    let mut output = io::stdout().lock();
    loop {
        let mut bytes = Vec::new();
        // Bound allocation even for a malicious unterminated frame.
        match std::io::Read::take(&mut input, (MAX_FRAME + 1) as u64).read_until(b'\n', &mut bytes)
        {
            Ok(0) => break,
            Err(_) => break,
            _ if bytes.len() > MAX_FRAME => break,
            _ => {}
        }
        let request: Value = match serde_json::from_slice(&bytes) {
            Ok(v) => v,
            Err(_) => {
                let _ = writeln!(
                    output,
                    "{}",
                    json!({"error":invalid("invalid native JSON frame")})
                );
                let _ = output.flush();
                continue;
            }
        };
        let result = dispatch(&request, &mut environment).await;
        if let Some(env) = &environment {
            eprintln!(
                "cogletnet_usage {}",
                json!({"method": request["method"], "usage": env.meter.totals()})
            );
        }
        let reply = match result {
            Ok(v) => json!({"result": v}),
            Err(e) => json!({"error":e,"operation_id":e.operation}),
        };
        if writeln!(output, "{reply}").is_err() || output.flush().is_err() {
            break;
        }
        if request["method"] == "close" {
            break;
        }
    }
}

async fn write_event(memory: CogletNet, row: Value, deadline: Instant) -> Value {
    let id = row["id"].as_str().expect("prevalidated event");
    let mut data = MemoryInput::text(row["text"].as_str().expect("prevalidated text"));
    data.metadata = serde_json::from_value(row["metadata"].clone()).expect("prevalidated metadata");
    let key = OperationKey::new(id).expect("prevalidated key");
    let ingested = match tokio::time::timeout_at(deadline, memory.ingest(data, key)).await {
        Ok(Ok(write)) => write,
        Ok(Err(error)) => return failure(id, None, error),
        Err(_) => {
            return failure(
                id,
                None,
                Error::new(
                    ErrorKind::OperationOutcomeUnknown,
                    "ingest execution window expired; replay the same event key",
                ),
            );
        }
    };
    let options = BuildExecutionOptions {
        timeout: Some(deadline.saturating_duration_since(Instant::now())),
    };
    match memory.build_with_options(ingested.operation, options).await {
        Ok(write) => {
            json!({"event_id":id,"state":write.state,"operation_id":write.operation,"source_ref":write.source_ref,"write":write})
        }
        Err(error) => failure(
            id,
            Some(ingested.source_ref),
            error.operation(ingested.operation),
        ),
    }
}

fn scoped_event(row: &Value) -> Result<EventInput> {
    let mut input = MemoryInput::text(string(row, "text")?);
    input.metadata = serde_json::from_value(row["metadata"].clone())?;
    Ok(EventInput {
        event_id: string(row, "id")?.to_owned(),
        event_sequence: row["event_sequence"]
            .as_u64()
            .ok_or_else(|| invalid("event_sequence required"))?,
        role: input.metadata.get("role").cloned(),
        speaker: None,
        occurred_at: serde_json::from_value(row["occurred_at"].clone())?,
        input,
    })
}

async fn write_scopes(memory: &CogletNet, rows: &[Value], deadline: Instant) -> Result<Value> {
    // Validate the complete batch before publishing. Append a scope's events
    // together; a replacement is an ordered boundary within that scope.
    let mut groups: Vec<(ScopeKey, Option<u64>, Vec<usize>)> = Vec::new();
    for (i, row) in rows.iter().enumerate() {
        let scope: ScopeKey = serde_json::from_value(row["scope"].clone())?;
        scope.validate()?;
        scoped_event(row)?;
        let version = row.get("expected_source_version")
            .map(|value| value.as_u64().filter(|v| *v > 0)
                .ok_or_else(|| invalid("expected_source_version must be positive")))
            .transpose()?;
        if version.is_none() {
            if let Some((_, None, indices)) = groups.iter_mut().rev().find(|g| g.0 == scope) {
                indices.push(i);
                continue;
            }
        }
        groups.push((scope, version, vec![i]));
    }
    let mut results: Vec<Value> = rows.iter().map(|row|
        json!({"event_id":row["id"],"state":"NotDispatched","operation_id":null})
    ).collect();
    for (scope, version, indices) in groups {
        if Instant::now() >= deadline {
            break;
        }
        let events = indices.iter().map(|i| scoped_event(&rows[*i])).collect::<Result<Vec<_>>>()?;
        // Content-based keys make a replay of an Unknown batch idempotent,
        // while a replacement gets a new key including its expected version.
        let key = OperationKey::new(format!("memeval-scope-{}", hash(&(&scope, &events, version))?))?;
        // Scheduled adopts the inputs and returns a durable operation identity
        // before any model build. Do not cancel it with a model-wait timeout.
        let write = async {
            if let Some(expected_source_version) = version {
                let event = events.into_iter().next().expect("replacement has one event");
                let event_id = event.event_id.clone();
                memory.replace_event(scope.clone(), &event_id, event,
                    ReplaceEventOptions { operation_key: key, expected_source_version, wait: WaitMode::Scheduled }).await
            } else {
                memory.append_events(scope.clone(), events,
                    AppendOptions { operation_key: key, wait: WaitMode::Scheduled }).await
            }
        }.await;
        match write {
            Ok(write) => {
                let group = indices.iter().map(|i| rows[*i].clone()).collect::<Vec<_>>();
                match wait_scoped(memory, write.operation, &group, deadline).await {
                    Ok(reply) => for (i, result) in indices.into_iter().zip(reply["events"].as_array().unwrap()) {
                        results[i] = result.clone();
                    },
                    Err(error) => for i in indices {
                        results[i] = failure(string(&rows[i], "id")?, None, error.clone().operation(write.operation));
                    },
                }
            }
            Err(error) => {
                for i in indices {
                    results[i] = failure(string(&rows[i], "id")?, None, error.clone());
                }
            }
        }
        if results.iter().any(|r| r["state"] != "Ready" && r["state"] != "NotDispatched") {
            break;
        }
    }
    Ok(json!({"events":results,"all_ready":results.iter().all(|r|r["state"]=="Ready")}))
}

fn reader_result(result: SearchResult, allowed: &[SourceId], representation: Representation) -> Result<Value> {
    let mut value = serde_json::to_value(&result)?;
    if representation == Representation::LeafMemory {
        for (item, row) in result.items.iter().zip(value["items"].as_array_mut().unwrap()) {
            let memory = item.memory.as_ref().ok_or_else(|| invalid("LeafMemory item missing memory"))?;
            let mapped = result.context_view.memories.iter().find(|m| m.reference == item.reference)
                .ok_or_else(|| invalid("LeafMemory item missing context mapping"))?;
            if mapped.layers.len() != memory.len() || mapped.layers.is_empty() {
                return Err(invalid("LeafMemory layer mapping incomplete"));
            }
            let mut sources = std::collections::BTreeSet::from([item.source.id]);
            for layer in &mapped.layers {
                let body = result.context_view.visible.iter().find(|body| body.reference == layer.reference)
                    .ok_or_else(|| invalid("LeafMemory layer missing visible body"))?;
                sources.extend(body.source_closure());
            }
            if sources.iter().any(|source| !allowed.contains(source)) {
                return Err(invalid("LeafMemory context contains source outside allowlist"));
            }
            // Use the upstream context renderer, including citation metadata.
            // Do not replace layers with source text or the legacy path string.
            row["reader_text"] = json!(cogletnet::algorithms::render_leaf_memories(std::slice::from_ref(memory))?);
            row["reader_sources"] = serde_json::to_value(sources)?;
        }
    } else if representation == Representation::Tree {
        if result.context_view.nodes.len() != result.items.len() {
            return Err(invalid("Tree item missing node context mapping"));
        }
        for ((item, mapped), row) in result.items.iter().zip(&result.context_view.nodes)
            .zip(value["items"].as_array_mut().unwrap())
        {
            if item.memory.is_some() || mapped.reference != item.reference {
                return Err(invalid("Tree node context mapping mismatch"));
            }
            let mut sources = Vec::new();
            for source in &mapped.sources {
                if !sources.contains(&source.id) {
                    sources.push(source.id);
                }
            }
            if !sources.contains(&item.source.id) || sources.iter().any(|source| !allowed.contains(source)) {
                return Err(invalid("Tree node contains source outside allowlist or missing representative source"));
            }
            // A Tree hit represents its own body and all of its covered leaf
            // sources, not a borrowed ancestor path or just the anchor source.
            row["reader_text"] = json!(item.text);
            row["reader_sources"] = serde_json::to_value(sources)?;
            row["level"] = json!(mapped.level);
            row["recorded"] = serde_json::to_value(&mapped.recorded)?;
        }
    }
    Ok(value)
}

async fn dispatch(request: &Value, environment: &mut Option<LiveEnvironment>) -> Result<Value> {
    let method = string(request, "method")?;
    if method == "open" {
        if environment.is_some() {
            return Err(invalid("already open"));
        }
        let namespace = Some(string(request, "namespace")?.to_owned());
        *environment = Some(if request["scheduled_writes"] == true {
            let (sender, mut receiver) = tokio::sync::mpsc::unbounded_channel();
            let mut prepared = LiveEnvironment::prepare(namespace).await?;
            prepared.adapters.scheduler = Some(Arc::new(BuildQueue(sender)));
            let live = prepared.open().await?;
            let memory = live.memory.clone();
            tokio::spawn(async move {
                while let Some(operation) = receiver.recv().await {
                    // Status/error are durably recorded by build; the waiter
                    // reads them. No host model retries or identity changes.
                    let _ = memory.build(operation).await;
                }
            });
            live
        } else {
            LiveEnvironment::open(namespace).await?
        });
        return Ok(json!({}));
    }
    let env = environment
        .as_ref()
        .ok_or_else(|| invalid("environment not open"))?;
    match method {
        "add_batch" | "add" => {
            let rows = if method == "add" {
                vec![request.clone()]
            } else {
                request["events"]
                    .as_array()
                    .ok_or_else(|| invalid("events required"))?
                    .clone()
            };
            let concurrency = if method == "add" {
                1
            } else {
                request["concurrency"]
                    .as_u64()
                    .ok_or_else(|| invalid("concurrency required"))? as usize
            };
            if !(1..=64).contains(&concurrency) {
                return Err(invalid("invalid ingest concurrency"));
            }
            let mut ids = std::collections::BTreeSet::new();
            for row in &rows {
                let id = string(row, "id")?;
                OperationKey::new(id)?;
                if !ids.insert(id) {
                    return Err(invalid("duplicate event id in batch"));
                }
                string(row, "text")?;
                let _: std::collections::BTreeMap<String, String> =
                    serde_json::from_value(row["metadata"].clone())?;
            }
            let deadline = Instant::now() + window(request)?;
            match request.get("write_mode").and_then(Value::as_str) {
                Some("scope_events") => return write_scopes(&env.memory, &rows, deadline).await,
                None | Some("legacy") => {},
                _ => return Err(invalid("unknown write mode")),
            }
            let mut pending = rows.clone().into_iter().enumerate();
            let mut tasks = tokio::task::JoinSet::new();
            let mut task_rows = std::collections::HashMap::new();
            let mut results: Vec<Value> = rows
                .iter()
                .map(|r| json!({"event_id":r["id"],"state":"NotDispatched","operation_id":null}))
                .collect();
            let mut stopped = false;
            loop {
                while !stopped && Instant::now() < deadline && tasks.len() < concurrency {
                    let Some((i, row)) = pending.next() else {
                        break;
                    };
                    let memory = env.memory.clone();
                    let task =
                        tasks.spawn(async move { (i, write_event(memory, row, deadline).await) });
                    task_rows.insert(task.id(), i);
                }
                let Some(joined) = tasks.join_next_with_id().await else {
                    break;
                };
                match joined {
                    Ok((task_id, (i, result))) => {
                        task_rows.remove(&task_id);
                        stopped |= result["state"] != "Ready";
                        results[i] = result;
                    }
                    Err(error) => {
                        stopped = true;
                        if let Some(i) = task_rows.remove(&error.id()) {
                            results[i] = failure(
                                rows[i]["id"].as_str().unwrap(),
                                None,
                                Error::new(
                                    ErrorKind::OperationOutcomeUnknown,
                                    "native write task interrupted; replay the same event key",
                                ),
                            );
                        }
                    }
                }
                eprintln!(
                    "cogletnet_usage {}",
                    json!({"method":"add", "usage":env.meter.totals()})
                );
            }
            Ok(json!({"events":results,"all_ready":results.iter().all(|r|r["state"]=="Ready")}))
        }
        "usage" => Ok(serde_json::to_value(env.meter.totals())?),
        "snapshot_manifest" | "snapshot_manifest_view" => {
            let snapshot: SnapshotId = string(request, "snapshot_id")?
                .parse()
                .map_err(|_| invalid("invalid snapshot ID"))?;
            if method == "snapshot_manifest_view" {
                Ok(serde_json::to_value(
                    env.memory.snapshot_manifest_view(snapshot).await?,
                )?)
            } else {
                Ok(serde_json::to_value(
                    env.memory.snapshot_manifest(snapshot).await?,
                )?)
            }
        }
        "get" => {
            let source: SourceId = string(request, "source_id")?
                .parse()
                .map_err(|_| invalid("invalid source ID"))?;
            let version: Option<u64> = serde_json::from_value(request["version"].clone())?;
            Ok(serde_json::to_value(
                env.memory
                    .get(SourceRef {
                        id: source,
                        version,
                    })
                    .await?,
            )?)
        }
        "operation_status" | "resume" | "audit_build" | "build_progress" | "wait_operation" => {
            let operation: OperationId = string(request, "operation_id")?
                .parse()
                .map_err(|_| invalid("invalid operation ID"))?;
            match method {
                "wait_operation" => {
                    let rows = request["events"].as_array().ok_or_else(|| invalid("events required"))?;
                    wait_scoped(&env.memory, operation, rows, Instant::now() + window(request)?).await
                }
                "resume" if request["scheduled"] == true => {
                    let status = env.memory.operation_status(operation).await?;
                    if matches!(status.state, OperationState::Failed | OperationState::Unknown) {
                        let memory = env.memory.clone();
                        let mut task = tokio::spawn(async move { memory.resume(operation).await });
                        // Do not poll the stale Failed record before resume
                        // acquires its lease. A timed-out JoinHandle detaches;
                        // subsequent wait_operation observes the same build.
                        match tokio::time::timeout(window(request)?, &mut task).await {
                            Ok(Ok(result)) => { result?; },
                            Ok(Err(_)) => return Err(Error::new(ErrorKind::OperationOutcomeUnknown,
                                "scope recovery task interrupted").operation(operation)),
                            Err(_) => {},
                        }
                    }
                    Ok(json!({"operation_id":operation,"scheduled":true}))
                }
                "build_progress" => Ok(serde_json::to_value(
                    env.memory.build_progress(operation).await?,
                )?),
                "operation_status" => Ok(serde_json::to_value(
                    env.memory.operation_status(operation).await?,
                )?),
                "audit_build" => Ok(serde_json::to_value(
                    env.memory.build_audit(operation).await?,
                )?),
                _ => Ok(serde_json::to_value(
                    env.memory
                        .resume_with_options(
                            operation,
                            BuildExecutionOptions {
                                timeout: Some(window(request)?),
                            },
                        )
                        .await?,
                )?),
            }
        }
        "search" => {
            let representation = match request.get("representation").and_then(Value::as_str) {
                None | Some("FullPath") => Representation::FullPath,
                Some("LeafMemory") => Representation::LeafMemory,
                Some("Tree") => Representation::Tree,
                _ => return Err(invalid("unsupported search representation")),
            };
            let options = SearchOptions {
                limit: request["limit"]
                    .as_u64()
                    .ok_or_else(|| invalid("limit required"))? as usize,
                sources: serde_json::from_value(request["sources"].clone())?,
                representation,
                mode: SelectionMode::Jev,
                failure_policy: FailurePolicy::Strict,
                trace: request
                    .get("trace")
                    .map(|value| {
                        value
                            .as_bool()
                            .ok_or_else(|| invalid("trace must be a boolean"))
                    })
                    .transpose()?
                    .unwrap_or(false),
                ..SearchOptions::default()
            };
            if options.sources.is_empty() {
                return Err(invalid("empty source allowlist"));
            }
            let allowed = options.sources.clone();
            let result = env.memory.search(string(request, "query")?, options).await?;
            reader_result(result, &allowed, representation)
        }
        "close" => Ok(serde_json::to_value(
            env.memory
                .shutdown(ShutdownMode::Drain, Duration::from_secs(30))
                .await?,
        )?),
        _ => Err(invalid("unknown native method")),
    }
}
