# Historical adapter protocol v1

This is the Python prototype contract, not the current Go JSON-RPC v2 interface. Keep three layers separate: dataset importers normalize episodes/cases; SUT adapters receive only allowed observations and questions; scorers retain answers, evidence labels, and categories. A trusted plugin interface is not a security boundary.

## Normalized input

```json
{
  "schema_version":"1.0",
  "suite_id":"dataset-and-protocol-version",
  "provenance":{"source_url":"UPSTREAM","source_revision":"COMMIT","local_input_sha256":"HASH"},
  "episodes":[{
    "id":"PRIVATE-EPISODE-ID",
    "events":[{
      "source_id":"PRIVATE-SOURCE-ID","owner":"subject","session":"session",
      "content":"UNMODIFIED SOURCE TEXT","role":"user","attributes":{}
    }],
    "cases":[{
      "id":"PRIVATE-CASE-ID","kind":"qa","query":"UNMODIFIED SOURCE QUESTION",
      "user":"subject","scope":[],"cutoff":1,
      "gold":{"answers":["PRIVATE ANSWER"],"evidence_ids":["PRIVATE-SOURCE-ID"],"evidence_unit":"event","exhaustive":false}
    }]
  }]
}
```

The example is a schema illustration, not a generated benchmark case. `cutoff` is an exclusive event-array bound. Only `events[:cutoff]` is ingested. Event and session evidence units must not be mixed without disclosure.

## SUT request

```json
{
  "method":"retrieve",
  "namespace":"paeval_RANDOM",
  "payload":{
    "query":"SOURCE QUESTION",
    "user":"u_RANDOM",
    "scope":["domain:code"],
    "as_of":null,
    "budget":{"top_k":10,"max_context_chars":100000},
    "query_invocation_id":"RANDOM",
    "output_format":"retrieval"
  }
}
```

Gold, question_type, raw question_id, has_answer, and the complete upstream row are withheld. Source/session/user IDs are pseudonymized per independent case; allowed aliases, content, roles, and source timestamps remain real. String-only adapters may serialize events if they disclose that transformation; no gold summaries or oracle labels may be added.

## HTTP transport

The historical bridge posts to the configured full endpoint. Remote transport requires HTTPS and explicit egress authorization, refuses redirects, and does not automatically retry writes. Methods are describe/reset/ingest/ready/retrieve/answer. Success returns `{"status":"ok","result":...}`; unsupported returns HTTP 501 or `{"status":"unsupported"}`; other failures remain errors.

## Describe and retrieval

```json
{"status":"ok","result":{"name":"your-pa","version":"PINNED-COMMIT","capabilities":["retrieve","answer"],"read_only_queries":true,"config":{"memory_backend":"NAME"}}}
```

```json
{"status":"ok","result":{"items":[{"id":"OPTIONAL INTERNAL MEMORY ID","text":"ACTUAL RETRIEVED TEXT","source_ids":["e_RANDOM"],"score":0.7}]}}
```

Version/config must be secret-free and identify model, embedding, and decision components. Returned source IDs must belong to ingested observations: unknown, future, or duplicate references are contract errors. A multi-source summary can reference multiple events, but reference overlap alone does not prove semantic support. Attaching all history IDs to a short answer is not evidence of useful retrieval; provenance needs independent review.

## Typed context and answers

```json
{"status":"ok","result":{"items":[],"personaPrelude":"ACTUAL PROFILE TEXT","tiers":{"pinnedFacts":[],"relevantBeliefs":[],"openQuestions":[]}}}
```

Typed context is a PMB diagnostic extension, not a requirement that every system use those internal tiers. Each object maps to one seed source. Unsupported fields must be declared unsupported rather than represented as successful empty output.

An answer returns `{"text":"ACTUAL ANSWER","abstain":false}` or `{"choice":"A"}`. An abstain flag does not establish factual correctness. Fixed-reader and native-answer experiments separate retrieval from the full system's answering model; the current Go runner does not enable native_answer.

Use independent namespaces, wait for observable writes at the ready barrier, avoid unsupported concurrent calls, and destroy task-owned state at close. Keep timing, unsupported operations, and errors visible instead of fabricating successful responses.
