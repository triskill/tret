# Future plans

Architecture plan for `tret` as a **fundament** for a flexible, agentic
harness: a platform where an agent loop is a BPMN file, the capabilities are
versioned job workers, and the engine owns the state machine.

This document assumes the defects listed in
[`mistakesToFix.md`](mistakesToFix.md) are fixed first — several plans below
are impossible to implement correctly on top of the current behaviour.

---

## 0. Where we stand

The current design is sound in its core idea and weak in its edges:

| Layer | Today | Verdict |
|---|---|---|
| Orchestration | ZenBPM engine (Raft cluster, partitioned storage, round-robin job distribution, BPMN error handling, delivery tokens) | keep — do not reimplement |
| Capabilities | `llm-call-v1`, `exec-command-v1`, third-party job types | right shape, needs a contract layer |
| Worker runtime | one process, one goroutine per job, no limits, no logs | too thin for production |
| State | only engine process variables | wrong place for transcripts/artifacts |
| Correctness | at-least-once, no idempotency, no fencing | **the gap that matters most** |
| Operations | `fmt.Println`, no metrics, no health, thin CLI | not operable |

The engine is doing the hard part well. `tret`'s job is to be the **capability
and correctness layer** on top of it: idempotent effects, a shared artifact
store, budgets, and observability.

### The one-line thesis

> The engine decides *what* happens next; `tret` guarantees that each step
> happens **exactly once** (where that is possible), **within budget**, and
> **without the state growing without bound**.

---

## 1. Design principles (the rules we will not break)

1. **No hidden loops.** A worker performs one capability. All iteration,
   branching and budgeting lives in BPMN. (Already true — keep it.)
2. **The engine owns the state machine.** `tret` never re-implements routing,
   waiting, timers or error boundaries.
3. **Effects are keyed, not hoped for.** Every side effect carries an
   idempotency key derived from the engine job key. No key, no guarantee —
   and the guarantee is stated in the contract.
4. **Failure classes are explicit.** A process error, a transient technical
   error and a budget stop are three different things with three different
   engine behaviours.
5. **State has a home.** Process variables carry *routing* data (small,
   bounded, inspectable). Transcripts, logs and artifacts live in a store
   referenced by a variable.
6. **Every capability is self-describing.** A worker publishes a machine
   readable contract; the CLI and the linter read it.
7. **The platform is boring and predictable.** No hidden retries, no hidden
   caches, no implicit ordering, no magic in the CLI. Surprises are bugs.
8. **Bounded everything.** Time, memory, output size, concurrency, spend,
   retries — all bounded and all visible.

---

## 2. Phase 0 — Correctness foundation (blocking)

Nothing else can be built safely before this. Target: two weeks.

**Status**: the side effects run detached from the job stream (`worker.Detach`),
every subscription asks for a lock which outlasts the work (`pkg/worker.Spec`),
the failures are split into transient and terminal ones (so the `retries` of a
task definition work again), and the journal of the finished steps exists
(`pkg/journal`, keyed on the engine job key, with a file store for one node).
Still open: a store several nodes can reach, the fencing of effects which
cannot be keyed, the renewal of the lock with `ExtendDeliveryLock`, and the
sandbox for the exec worker.

### 2.1 Execution identity and the idempotency journal

Done in `pkg/journal`: the interface, the guard which applies the protocol to
a worker, an in-memory store and a file store for one node. The store several
nodes can reach is still open.

```

```
type Key struct {
    JobKey   int64  // engine job key — the canonical identity of the step
    Business string // optional business key from the task headers
}

type Record struct {
    Key       Key
    State     State      // leased | done | failed
    Owner     string     // worker instance id
    LeaseTill time.Time
    Result    map[string]any
    Error     string
    Cost      Usage      // tokens, money
    StartedAt, FinishedAt time.Time
}
```

Worker protocol (all built-in and third-party workers follow it):

```
1. journal.Lookup(key)
     state == done   -> return the memoized result          (no side effect)
     state == leased -> not ours (another replica runs it) -> return a retryable failure
2. journal.Acquire(key, owner, lease)   -> we own the step
3. execute the side effect under a context detached from the stream
4. journal.Commit(key, owner, result)   -> durable, then return the result
```

Key properties:

* **Keyed on the job key**, never on `attempt` or `delivery token` — the
  engine's own documentation is explicit that those two repeat the effect.
* The lease prevents two replicas from executing the same step *at the same
  time*; the commit makes the result reusable by the next delivery.
* A crash between 3 and 4 leaves a `leased` record: the lease expires and the
  step is retried. That is the unavoidable "unknown outcome" window — it is
  *narrowed*, not removed, and the contract must say so.

Store backends (the `Journal` interface):

* `Memory` — in the process, for the tests.
* `File` — a directory, so the records survive the restart of a worker and
  are shared by the workers of one node. Done.
* `Postgres` — for multi-node deployments; `INSERT … ON CONFLICT DO NOTHING`
  gives the atomic acquire. Open.

### 2.2 Effects that cannot be made idempotent

For a side effect the world cannot dedupe (a card charge, an email), exactly
once is not achievable; the best available is:

* **at-most-once**: never execute if the outcome is unknown (fail closed, ask a
  human). Use the journal to record "started" *before* the effect and refuse
  to repeat it.
* **at-least-once**: execute again and accept the duplicate, but pass a
  business idempotency key to the downstream API so *it* can dedupe.

The job type declares which mode it uses (`side_effect: idempotent |
fenced | at_most_once | at_least_once`). `tret` refuses to start a worker whose
declared mode the deployment cannot satisfy.

### 2.3 Detach the side effect from the stream

`context.WithoutCancel(jobCtx)` + an explicit deadline, as described in
`mistakesToFix.md`. The worker keeps running when the stream breaks; the
result is committed to the journal; the redelivery picks it up and returns it
without re-executing. This is what turns a transport hiccup from "duplicate
side effect" into "slightly late result".

### 2.4 Lock discipline

* One registration helper that always sets a lock duration.
* Lock duration derived from the worker's declared worst-case runtime, not a
  constant.
* `ExtendDeliveryLock` renewal for work that may run long.
* Startup validation: `lock_duration > worst_case_runtime` or the server
  refuses to start.

### 2.5 Error taxonomy

`pkg/contract` defines the failure classes and every worker uses them:

| class | worker error | engine | BPMN |
|---|---|---|---|
| invalid request | `ErrorCode: invalid_input`, retries 0 | BPMN error | error boundary / incident |
| transient | no `ErrorCode` | spends a retry, applies `retries`/`retryBackoff` | normal path continues |
| invalid output | `ErrorCode: invalid_output`, retries 0 | BPMN error | error boundary / incident |
| budget stop | `ErrorCode: budget_exceeded`, retries 0 | BPMN error | routes to a cheaper path |
| cancelled (operator/engine) | no `ErrorCode` | spends a retry | normal path continues |

Acceptance: a BPMN with `retries="3" retryBackoff="PT5S"` sees exactly three
attempts for a transient provider failure, and one attempt for an invalid
request.

---

## 3. Phase 1 — State outside the engine

The engine is a *routing* substrate, not a data store. Measured today: the
whole process scope is copied into every job's input variables, and the full
worker output is stored on the job record as well. An agent loop that keeps
its transcript in a process variable therefore grows quadratically.

### 3.1 The artifact store

`pkg/artifacts`: content-addressed blobs (`sha256:<hex>`), pluggable backends
— local filesystem (default), S3/MinIO. Objects: large exec output, model
transcripts, tool results, files produced by a step.

### 3.2 The transcript service

`pkg/transcript` (or a separate `tret-ctx` service, see §9):

```
transcript_id -> ordered list of turns {role, content, tool_calls, usage}
```

The process variable holds a *reference*, not the content:

```json
{"transcript_id": "sha256:…", "turn": 7, "digest": "sha256:…"}
```

`llm-call-v1` gains an optional `context_ref` in its request:

```json
{"model_profile": "default", "context_ref": {"transcript_id": "…", "window": 20},
 "messages": [{"role": "user", "content": "…"}]}
```

The worker then assembles the prompt from the store (last N turns, or a
summarised prefix), sends it, appends the answer to the transcript, and returns
a **bounded** result (`content`, `usage`, `transcript_id`, `digest`).

Consequences:

* Engine storage per turn becomes O(1) instead of O(history).
* The prompt no longer has to be re-sent by the process author.
* A resumed or replayed instance can rebuild its context from the store.
* The same store is the natural place for the **cost ledger**.

### 3.3 Output spill

`exec-command-v1` returns `{stdout_ref, preview, truncated, exit_code, …}`
when the output exceeds a threshold (default 32 KiB stays inline; beyond that
it is spilled). Same for long LLM content.

### 3.4 Result envelope

Both workers return a common envelope so downstream tooling can treat them
uniformly:

```json
{"result": {...}, "usage": {...}, "artifacts": [{"ref": "sha256:…", "kind": "stdout"}],
 "cost": {"tokens": 812, "currency": "USD", "amount": 0.0031}}
```

---

## 4. Phase 2 — Budgets and cost control

BPMN counters can bound *counts*; they cannot bound *money*, because the token
usage is only known after the call.

### 4.1 What BPMN can do

A counter is a process variable, and a gateway cannot assign one — but an
**output mapping** can. `TODO.md`'s "budget as BPMN" recipe needs this concrete
mechanism.

**[verified against the live engine]** A service task whose output mapping
recomputes the counter increments it on every completion; an exclusive gateway
on the counter closes the loop:

```xml
<bpmn:serviceTask id="Count">
 <bpmn:extensionElements>
  <zenbpm:taskDefinition type="llm-call-v1" />
  <zenbpm:ioMapping>
   <zenbpm:input  source="=turn"     target="turn" />
   <zenbpm:output source="=turn + 1" target="turn" />
  </zenbpm:ioMapping>
 </bpmn:extensionElements>
 ...
</bpmn:serviceTask>
<bpmn:exclusiveGateway id="Gate">
 <bpmn:sequenceFlow id="Again" sourceRef="Gate" targetRef="Count">
  <bpmn:conditionExpression>=turn &lt; 3</bpmn:conditionExpression>
 </bpmn:sequenceFlow>
```

Measured: three executions, final `turn = 3`, instance completed — no extra
worker, no script task (the engine rejects `scriptTask`:
`pkg/bpmn/unsupported_elements_test.go`).

This is a *loop* budget and it works. It is not a *spend* budget.

### 4.2 The server-side budget guard

New: a budget guard consulted by `llm-call-v1` **before** the call:

```
scope: per instance | per business key | per tenant | per profile
limits: max tokens, max cost, max wall-clock, max tool calls, max turns
action: allow | refuse (ErrorCode budget_exceeded) | degrade (switch profile)
```

The guard keeps its own ledger in the store (Phase 1), because the engine's
variables cannot be trusted to be updated atomically with the call.

A refused call raises a catchable BPMN error, so the process can route to a
cheaper model, ask a human, or stop — this is what makes an agent loop
*governable* rather than merely *runnable*.

---

## 5. Phase 3 — Horizontal scalability

### 5.1 What the engine already gives us

* The engine is a Raft cluster with partitioned RqLite storage; it scales
  horizontally on its own.
* The job manager distributes jobs **round-robin over the clients subscribed
  to a job type** (`internal/cluster/jobmanager/server.go:468`). Several
  workers of the same type are a supported topology, not a hack.
* The engine is the only stateful component; workers are (by design)
  stateless.

So the platform is *already* horizontally scalable **as a job-execution
model**. What is missing is everything around it.

### 5.2 What blocks it today

| blocker | why it blocks | fix |
|---|---|---|
| client ID = `tret-<pid>` | PID 1 in every container → replicas collapse into one round-robin entry → one replica gets nothing | unique ID: host + pid + random (`mistakesToFix.md`, S4) |
| no idempotency | a redelivery to another replica re-executes the effect | journal (Phase 0) |
| shared `exec_root` | jobs of different replicas share one directory; results are not reproducible | per-job workspace in a shared artifact store, or a container per job |
| transcript in process variables | O(n²) storage; the instance cannot be moved or resumed elsewhere | transcript store (Phase 1) |
| lock duration fixed at 5 min, never renewed | a slow job is delivered to a second replica while the first still runs | lock policy + renewal (Phase 0) |
| credentials in a local config file | every replica needs the same secrets; rotation is a redeploy | secret store / mounted secrets + rotation |
| no backpressure | the engine default is 10 active jobs per subscription; one replica saturates long before the cluster does | explicit `WithMaxActiveJobs`, configurable per profile |
| no health/readiness | an orchestrator cannot tell a broken replica from an idle one | `/healthz`, `/readyz`, `tret doctor` |
| in-memory transport state | a reconnect storm hammers the engine | backoff (the zenclient has it) + jitter |

### 5.3 Target topology

```
            ┌──────────────┐
   clients  │ ZenBPM UI    │
            └──────┬───────┘
                   │
        ┌──────────▼───────────┐        ┌──────────────┐
        │ ZenBPM cluster (raft)│◄───────┤ tret worker  │  × N replicas
        │  partitions + rqlite │  gRPC  │  (llm/exec)  │  unique client id
        └──────────┬───────────┘        └──────┬───────┘
                   │ REST                       │
        ┌──────────▼───────────┐        ┌──────▼───────┐
        │ Postgres (journal,  │◄───────┤ artifacts    │
        │  ledger, transcripts)│        │ (S3/files)   │
        └──────────────────────┘        └──────────────┘
```

Scaling unit: **the worker replica**. The engine scales on its own axis
(partition count, replicas).

### 5.4 Can we approach idempotency?

Yes, and this is the core of the answer.

* **Exactly-once *effect*** — achievable when the effect can be keyed and the
  key can be checked before execution. The journal gives us that for every
  effect we perform ourselves (LLM call, shell command), because the result
  is memoized on the job key and a redelivery returns it.
* **Exactly-once *world effect*** — only when the downstream system itself
  deduplicates on a key we pass (payment provider idempotency keys, upserts by
  primary key). For those, `tret` passes the business key through to the
  effect.
* **Exactly-once for arbitrary shell commands** — **not achievable**. A
  command can fail between "side effect done" and "result recorded". The best
  available is a *narrowed* window plus an explicit contract: the job type
  declares `side_effect: at_least_once`, the journal records the attempt, and
  operators are told that a step may run twice.

The honest position, which the docs should state: **`tret` provides
exactly-once for effects it can key and fence; for arbitrary effects it
provides at-most-once or at-least-once, chosen explicitly per job type, with
the duplicate window made visible (journal records + metrics).**

### 5.5 Order of work

```
Phase 0 (correctness)  ──►  Phase 1 (state)  ──►  Phase 3 (scale)
        │                         │
        └────────► Phase 2 (budgets) ──────────► Phase 4 (observability)
```

Scaling before Phase 0 multiplies side effects; scaling before Phase 1
multiplies storage waste.

---

## 6. Phase 4 — Observability and operations

### 6.1 Logging

`log/slog`, JSON, one line per job event, correlated by:

```
job_key, instance_key, element_id, job_type, delivery_token, attempt,
worker_id, client_id, profile, duration_ms, outcome, error_code
```

### 6.2 Metrics (Prometheus)

* `tret_jobs_total{type,outcome}` — completed / failed / retried / budget-stopped.
* `tret_job_duration_seconds{type}` — histogram.
* `tret_jobs_in_flight{type}` — gauge.
* `tret_llm_tokens_total{profile,model}`, `tret_llm_cost_total{profile}`.
* `tret_provider_errors_total{profile,status}`.
* `tret_journal_hits_total{type}` — **idempotency effectiveness**: the share
  of deliveries that were served from the journal instead of re-executing.
  This is the single most important metric for the correctness story.
* `tret_exec_failures_total{reason}`.

### 6.3 Tracing

The engine emits OTel spans for jobs and for the process itself, but it does
**not** currently hand the trace context to the worker: the job headers are
the static `zenbpm:taskHeaders` of the task definition, and no propagation
context is injected into them. The worker therefore starts a *new* trace and
links it to the job key.

Two options:

* **Link** — the worker starts a span with `links: [engine job span]`. Cheap,
  works today, gives one searchable place per step.
* **Propagate** — ask the engine to inject the W3C trace context into the job
  headers (an engine change, or a header the process writes itself). Gives one
  continuous trace *BPMN token → worker → provider HTTP call*.

Until the engine propagates it, prefer **link** and make the job key a
first-class attribute on every span, so a trace can always be assembled from
the job key.

### 6.4 CLI

```
tret doctor                 # engine, journal, profiles, exec root, clock skew
tret status                 # engine + worker health
tret instance <key>         # state, variables, timeline
tret jobs <instance>        # job list with attempts, locks, incidents
tret incidents <instance>   # incident list
tret resolve <incident>     # resolve, with optional retries
tret complete <job>         # answer a userTask from the terminal (human in the loop)
tret capabilities           # the contract manifests of the registered workers
tret version
```

`tret complete` closes the `TODO.md` item "Generic human-task client": a
`userTask` is an ordinary job (`user-task-type` by default), so the same
`ListJobs`/`CompleteJob` path serves it. It must be agent-loop agnostic and
usable over SSH.

### 6.5 Runbooks

Operator procedures for: a stuck instance, an incident storm, a provider
outage, a duplicate-effect alert from the journal, and a budget breach.

---

## 7. Phase 5 — Worker contracts (making the platform universal)

The current "write a worker" instructions are prose in `docs/workers.md`. That
is the weakest part of the "universal" story: every new worker re-invents
validation, error codes, retries and tests.

### 7.1 A machine-readable contract

Each worker publishes a manifest:

```json
{
  "job_type": "llm-call-v1",
  "version": 1,
  "input": {"$ref": "…/llm_request.schema.json"},
  "output": {"$ref": "…/llm_result.schema.json"},
  "error_codes": {
    "invalid_input":    {"retryable": false, "class": "process"},
    "provider_busy":    {"retryable": true,  "class": "transient"},
    "provider_error":   {"retryable": true,  "class": "transient"},
    "invalid_output":   {"retryable": false, "class": "process"},
    "budget_exceeded":  {"retryable": false, "class": "policy"}
  },
  "side_effect": "idempotent",
  "max_runtime_ms": 120000,
  "idempotency": {"key": "job_key", "journal": true},
  "capabilities": ["network:https", "secret:profile:<name>"],
  "concurrency": {"default_max_active_jobs": 10}
}
```

`tret capabilities` prints it; a BPMN linter checks that every referenced job
type is known, that the mappings match the schema, that the declared error
codes are handled by the process, and that the retry semantics of the task
definition match the error class.

### 7.2 A contract test suite

`pkg/workertest`: a table-driven suite every worker must pass —

* rejects unknown fields, missing required fields, out-of-range values;
* performs no side effect when validation fails;
* returns the documented output shape;
* maps each error condition to the documented error code and retry class;
* **is idempotent under redelivery** (run the handler twice with the same job
  key → the side effect happened once);
* honours its declared timeout and output cap.

`tret new worker my-tool-v1` scaffolds the worker, the manifest and the tests.

### 7.3 Decoupling from the engine

`pkg/worker.Registry` currently exposes `proto.WaitingJob` and
`*zenclient.WorkerError`, so every worker is pinned to the engine's Go API
version. Introduce `pkg/contract`:

```go
type Job struct { Key int64; InstanceKey int64; ElementID, Type string
                  Attempt int32; DeliveryToken int64; Inputs map[string]any
                  Headers map[string]string; LockUntil time.Time }
type Result struct { Variables map[string]any; Usage Usage; Artifacts []Artifact }
type Failure struct { Code string; Retryable bool; Err error }
```

with a single adapter to the zenclient. Workers then depend on `tret`, not on
`zenbpm` — which is what makes the platform *universal* and lets the engine
client be swapped or the workers embedded elsewhere.

### 7.4 Tool routing — the honest trade-off

The engine's job type is a **static attribute** of `zenbpm:taskDefinition`
(`docs/reference/bpmn/supported-elements/activities/tasks/service-task.md`).
It cannot be an expression. Therefore:

* **Pattern A — one task per tool.** `tool-search-v1`, `tool-write-v1`, … each
  with its own service task, plus an exclusive gateway per tool call. Fully
  declarative, fully inspectable, but the diagram grows with the tool count and
  the routing conditions multiply.
* **Pattern B — one dispatch worker.** `tool-dispatch-v1` receives the model's
  tool call and routes it internally. One task, any number of tools — but the
  routing policy moves back into Go, which is exactly what the platform
  philosophy avoids.

Recommendation: **Pattern A by default**, with a *narrow* Pattern B escape
hatch (`tool-dispatch-v1` that only maps a tool name to another job type and
delegates the work to a worker of that type — the policy stays in BPMN, only
the lookup moves). Document both, with the cost of each, and put the decision
in the BPMN authoring guide.

---

## 8. Phase 6 — Security and multi-tenancy

The current threat model is "trusted process authors, private engine, trusted
local users" (`docs/workers.md`). That is fine for a POC and must be stated as
a **production blocker**.

* **Transport:** mTLS to the engine gRPC and the REST API; the current
  `insecure.NewCredentials()` (`internal/zen/grpc.go:13`) is fine only on a
  loopback.
* **Secrets:** API keys in a config file are a rotation and audit problem.
  Move to mounted secrets / a secret manager, with hot reload.
* **The exec worker:** this is the sharp edge. Options: (a) container per job
  (no network by default, CPU/memory/pids limits, read-only rootfs, dropped
  capabilities), (b) a dedicated sandbox host, (c) an explicit
  "trusted-single-tenant" mode with a loud startup warning and no ambiguity
  in the docs. Today it is (c) without saying so.
* **Provenance:** record which process definition version, which worker and
  which identity caused each side effect — the journal is the natural place.
* **Prompt/command injection:** a loop that feeds model output into
  `exec-command-v1` is an RCE by construction. The platform must state this
  loudly, and the sandbox is the only real mitigation.

---

## 9. Open questions (decide before Phase 1)

1. **Where does the journal live?** Beside the workers (Postgres) or inside the
   engine cluster? Inside would give us transactions with the engine state;
   beside keeps the engine untouched and the workers portable. *Current
   leaning: beside, in Postgres — the engine's storage API is not a
   general-purpose KV store and we do not want to fork the engine.*
2. **Is `tret` a service or a library?** Today it is a binary plus public
   packages. A `tret-ctx` service for transcripts/budgets implies a service;
   an embedded `pkg/transcript` implies a library. *Leaning: library-first,
   with the service as a thin deployment of the same packages.*
3. **Who owns the artifact store?** If the platform owns it, workers must be
   able to write to it → a capability model (`capabilities: ["artifact:write"]`).
4. **Do we support multi-tenancy in the platform, or is tenancy a
   deployment concern?** The budget guard is per-tenant-aware, which suggests
   multi-tenancy is real. If not, say so and keep the guard simple.
5. **Compatibility policy for job types.** `llm-call-v1` is versioned in its
   name; what is the deprecation story? (Proposed: a type version is
   immutable; breaking changes get a new name; the manifest records
   `supersedes`.)
6. **How much BPMN do we want in the loop?** The engine supports more than the
   current example uses: timer events (start/intermediate/boundary, both
   interrupting and not) and multi-instance activities (parallel and
   sequential) are supported, as are call activities, sub-processes, DMN
   decisions and FEEL expressions. That is enough to express a full agent loop
   declaratively — parallel tool calls, timeouts, escalation to a human.
   *Decision: the reference loop should use these engine features rather than
   inventing worker-side coordination.*

---

## 10. Roadmap summary

| Phase | Theme | Outcome | Depends on |
|---|---|---|---|
| 0 | Correctness | idempotent effects, detached work, lock discipline, error taxonomy — *mostly done* | — |
| 1 | State outside the engine | artifact store, transcript store, bounded outputs | 0 |
| 2 | Budgets | BPMN counter recipe + server-side spend guard | 1 |
| 3 | Horizontal scale | unique client IDs, shared journal/store, backpressure, health | 0, 1 |
| 4 | Observability | logs, metrics, traces, full CLI, runbooks | 0 |
| 5 | Worker contracts | manifests, contract tests, engine decoupling, tool routing guide | 0 |
| 6 | Security | mTLS, secrets, sandboxed exec, provenance | 1 |
| 7 | Reference loop | `examples/agent-loop/` + authoring guide + `tret new` | 0–5 |

**Definition of done for the platform (v1):**

* A new capability is a worker + a manifest + a contract test — no change to
  the core, no change to the CLI.
* A new loop is a BPMN file — no Go code.
* A worker replica can be added, removed or replaced at any time without
  duplicating a side effect.
* Every side effect is attributable: which definition, which job, which
  worker, which attempt, which cost.
* Every run is bounded: time, tokens, money, output size, concurrency.
* An operator can answer, from the CLI alone: *what is running, what failed,
  what did it cost, and did anything happen twice?*
