# Mistakes to fix

Code review of the current `main` (commit `0cb28f6`), verified against a live
ZenBPM v1.9.0 engine (`make up`, `TRET_INTEGRATION=1 go test ./internal/integration`
passes — the baseline is green).

Every finding below is either reproduced by reading the code plus the engine
sources (`github.com/pbinitiative/zenbpm@v1.9.0`) or measured against the live
engine. Measurements are marked **[measured]**.

Severity: **S1** = data corruption / duplicated side effects / money,
**S2** = breaks a documented contract or blocks scaling,
**S3** = correctness/ergonomics bug, **S4** = hygiene.

---

## S1 — In-flight work is aborted when the job stream breaks, and then repeated

**Fixed** (commit `5063e9e`): the side effect runs under `worker.Detach`,
which gives the work its own deadline and ignores the cancellation of the
stream.

`pkg/llm/worker.go:202` and `pkg/shellexec/worker.go:91` pass the job context
straight into the side effect. That context is the **gRPC stream context**:
`zenclient.Worker.handleJob` calls `w.f(stream.Context(), job)`, and
`zenclient.Worker.connect()` cancels the previous stream context
(`previousCancel()`) before opening the replacement stream.

Consequence: every reconnect of the job stream — engine restart, leader
change, network blip — **cancels the running LLM request / shell command**.

**[measured]** Engine restart while a job was in flight:

```
[09:00:47] delivery 1 starts (job 2123687983444922369, lockUntil in 1m59s)
ERROR zenclient: job stream closed by server (EOF); attempting to reconnect
[09:00:58] delivery 1: stream context cancelled: context canceled
INFO  zenclient: job 2123687983444922369 finished after its stream was closed;
      discarding result, job will be redelivered
[09:01:03] delivery 2 starts (job 2123687983444922369, …)
```

For `llm-call-v1` the provider has usually already computed and billed the
completion when the request is aborted; the answer is thrown away and the job
is executed again. For `exec-command-v1` the command receives `SIGTERM`
mid-flight (`pkg/shellexec/runner.go:135`) — a half-applied side effect — and
is then run again on redelivery.

**Fix.** Never bind a side effect to the stream lifetime. Detach the context
and give the work its own deadline:

```go
// pkg/llm/worker.go, pkg/shellexec/worker.go
work, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobBudget)
defer cancel()
result, err := w.Client.Complete(work, profile, input)
```

The result is still lost when the stream dies (zenclient discards it), so this
fix only removes the *partial* execution. It must land together with the
idempotency journal below, otherwise the detached work makes the duplication
*more* likely, not less.

---

## S1 — The job lock is shorter than the worker's own worst case on every path but one

**Fixed** (commit `5063e9e`): `pkg/worker.Spec` refuses a subscription whose
lock does not outlast the declared runtime, `worker.Subscribe` is the safe
path for third-party workers, and the server validates the configured lock
against the runtime of the workers it serves.

**The renewal is fixed as well** (commit `7d25eb5`): `pkg/worker.Registry`
renews the lock of a delivered job while the job runs
(`zenclient.Worker.ExtendDeliveryLock`), and `worker.Subscribe` renews it
for third-party workers too. The renewal follows the deadline the engine
reports (`WaitingJob.LockUntil`) when the clocks of the worker and of the
partition leader agree, and counts the lock duration from the moment of
receipt when they do not. The renewal outlives the job stream: the engine
cancels the context of a delivery when the stream breaks, and the side
effect may well be running on.

**[measured]** The renewal against the live engine (a job of 30 s, a lock of
10 s, `internal/integration/renew_test.go`):

```
without the renewal: the side effect ran 3 times — the engine handed the
                      job out again every time the lock lapsed
with the renewal:    the side effect ran once
```

The engine's default lock duration is **30 s**
(`internal/config/config.go:177` `DefaultLockDurationMs: 30000`).

**[measured]** Registering a worker without an explicit lock duration:

```
default-registration lock remaining: 29s
```

`tret`'s own workers are allowed to run for **120 s** (`pkg/llm/client.go:49`
HTTP timeout, `pkg/shellexec/worker.go:23` `maxTimeout`). So:

* `internal/zen/grpc.go:24` — `Register()` → `RegisterWorker()` → engine default.
* `examples/classifier/worker/main.go:28` — same.
* every third-party worker that follows the documented example — same.
* `cmd/tret/server.go:60` — the only place that sets a lock (5 min), and even
  that is a constant unrelated to the job's actual runtime.

**[measured]** Same job, 5 s lock, 40 s handler — the engine keeps handing the
job out while the previous delivery is still running:

```
[08:59:30] delivery 1 starts   [08:59:45] delivery 4 starts
[08:59:35] delivery 2 starts   [08:59:50] delivery 5 starts
[08:59:40] delivery 3 starts   … 8 deliveries of one job in total
```

The engine's own documentation states the rule: *"Completions are not bound to
a delivery: a worker whose lock lapsed may still complete the job, and the
first completion ends it"* (`docs/reference/jobs.md`). Every one of those
deliveries ran the side effect.

**Fix.**

1. One helper that all registrations go through, which always sets a lock
   duration ≥ the worker's worst-case runtime
   (`zen.RegisterWorker(ctx, conn, id, fn, zenclient.WithJobType(t,
   zenclient.WithLockDuration(d)))`).
2. Make the lock duration a per-worker-type config value and **validate it at
   startup** against the worker's maximum runtime.
3. Renew the lock while the work runs (`zenclient.Worker.ExtendDeliveryLock`),
   so the guarantee does not depend on a static guess. **Done.**
4. The engine tells the worker the deadline (`WaitingJob.LockUntil`); use it
   instead of assuming. **Done.**

---

## S1 — No idempotency: every redelivery repeats the side effect

**Fixed** (commit `7534998`): `pkg/journal` stores the result of a step,
keyed on the engine job key, and a redelivery returns the stored result. The
store keeps its records on one node; a store several nodes can reach is open,
as is the fencing of effects which cannot be keyed.

Neither worker ever looks at `job.GetKey()`, `job.GetAttempt()` or
`job.GetDeliveryToken()`. There is no journal, no fence, no memoization.
Combined with the two findings above this means *the number of executions of a
side effect is the number of deliveries*, with no upper bound.

The engine tells us exactly what to key on (`docs/reference/jobs.md`):

> The engine cannot undo what a worker did before it failed […] an effect which
> must happen once per job is made idempotent on the job key, or on a key of
> the business operation it performs.
> Neither the `attempt` nor the `delivery_token` of a delivery is a substitute
> […] keying an effect on it, or on the job key and the attempt, would repeat
> the effect on every retry.

Note the second sentence: the *attempt* and the *delivery token* are **wrong**
keys — the job key (or a business key) is the right one.

**Fix.** A shared, durable result journal, outside the worker process:

```
key   = engine job key (+ optional business key from the task headers)
value = {state: running|done, owner, lease_until, result, error}
```

Worker protocol: look up → if `done`, return the memoized result; otherwise
take a lease, execute, write `done` + the result, return it. The journal must
be shared by all replicas (it is the component that makes horizontal scaling
safe — see `futurePlans.md` §2.1 and §5).

Until that exists, `docs/workers.md` must say plainly that **both built-in
workers are unsafe for non-idempotent effects** (billing, mail, file writes)
and that `exec-command-v1` in particular runs arbitrary commands with the
server's permissions.

---

## S2 — The BPMN `retries` attribute is dead for both built-in workers

**Fixed** (commit `75b078f`): the provider reports a typed error with its
status, a busy or failed provider is reported without an error code — so the
engine spends a retry — and a refused request keeps its catchable code.
Verified against the live engine.

`pkg/llm/worker.go` and `pkg/shellexec/worker.go` always attach an error code
(`invalid_input`, `provider_error`, `invalid_output`). The engine documents
(`pkg/bpmn/jobs_api.go`):

> With an error code it is a BPMN error […] **Retries are neither consulted nor
> spent.**

**[measured]** Same BPMN, `zenbpm:taskDefinition retries="3"`:

```
with error code : attempt 1 … (1 attempt total)
without error code: attempt 1, 2, 3 … (retries 3 → 2 → 1)
```

So `examples/classifier/classifier.bpmn`'s `retries="1"` never applies, and a
transient provider error (429, 500, a socket reset) becomes a BPMN error →
an incident → a dead loop. There is no way to ask for "retry this 3 times with
a backoff" while keeping the error catchable.

**Fix.** Split the taxonomy and make it a documented contract:

| class | example | worker error | engine behaviour |
|---|---|---|---|
| invalid request | missing `model_profile`, bad schema | `ErrorCode: "invalid_input"`, no retries | BPMN error, no retry |
| transient technical | 429, 5xx, dial timeout | **no** `ErrorCode` | spends a retry, `retryBackoff` applies |
| invalid model output | schema mismatch | `ErrorCode: "invalid_output"`, no retries | BPMN error, no retry |

`pkg/llm/client.go` must classify the provider status (429/5xx → transient)
instead of collapsing everything into `provider_error`. Also fix the comment in
`pkg/llm/worker.go:35` ("Zero retries disables scheduled retries") — with an
error code the retries are not *disabled*, they are *never consulted*.

---

## S2 — `exec-command-v1` is not safe to expose, and it blocks horizontal scaling

**Partly fixed** (commit `5063e9e`, `c84f571`): the shell reads no profile
file, so the environment of a command does not depend on the files in the
workspace, and with `exec_workspace: instance` every instance has a directory
of its own under the `exec_root`. The sandbox, the resource limits and the
network policy are still open.

* `exec_root` defaults to `"."` — the server's working directory
  (`internal/config/config.go:35`).
* Commands run with the server's permissions; there is no sandbox, no CPU /
  memory / disk limit, no network policy.
* **All jobs share one directory.** Concurrent jobs of different instances
  stomp on each other's files; two replicas on two nodes share nothing, so a
  process that assumes "the workspace" is not reproducible across replicas.
* `bash -lc` is a *login* shell: it sources `/etc/profile` and
  `~/.bash_profile` — and `HOME` is set to the workspace
  (`pkg/shellexec/runner.go:67`), so a file in the workspace changes the
  environment of every later command.
* `PATH` is set by tret and then possibly overwritten by `/etc/profile`.

**Fix.** Per-job workspace (`<exec_root>/jobs/<job key>`), `bash --noprofile
--norc -c`, an explicit minimal environment, and an explicit statement in
`docs/workers.md` that this is a *trusted, single-tenant* primitive. Container
based isolation is the follow-up (`futurePlans.md`, Phase 6).

---

## S2 — Misconfiguration is reported as a process error, and the config package kills the process

**Fixed** (commit `5063e9e`): `FromFlags` reports an error instead of
exiting, and the server validates every model profile at startup. The
"no api key → Ollama" heuristic is still open.

* `pkg/llm/worker.go:200` — an invalid **server** profile is returned as
  `invalid_input`, i.e. "the process sent a bad request". The process did
  nothing wrong; the operator configured the server wrong. A BPMN error
  handler would swallow it as a bad request.
* The server never validates the profiles it serves
  (`cmd/tret/server.go:27-40`), so a broken `api_base` is discovered by
  watching jobs fail, one incident at a time.
* `internal/config.FromFlags` calls `os.Exit(1)` on a config error and uses
  `flag.ExitOnError` — the package cannot be used as a library, cannot be
  unit tested for failure paths, and cannot be embedded in another binary.
* The "no api key → Ollama" heuristic (`cmd/tret/server.go:27-30`) is a trap: a
  local vLLM/Ollama endpoint that wants a dummy key silently talks to
  `api.openai.com`.

**Fix.** Validate the whole configuration at startup (profiles parse, the
model is set, the base URL is a valid http(s) URL, the exec root exists) and
fail fast with a precise message. `FromFlags` should return `(*Config, error)`.

---

## S3 — The local `config.yaml` is stale and breaks the server immediately

**Fixed** (commit `c84f571`): the loader is strict, so the stale keys are
reported with their lines instead of being ignored, and the local file is
refreshed.

`config.yaml` (gitignored, but present in the working copy and copied from an
older revision) still contains the removed agent-loop keys
(`deploy_agent`, `workers: agent-budget,agent-decide,agent-execute,…`,
`state_dir`, `workspace`, `max_tool_calls`).

```
$ go run ./cmd/tret server -config config.yaml
unknown built-in worker "agent-budget"
```

The unknown keys are silently ignored because `yaml.Unmarshal` is not strict
(`internal/config/config.go:52`) — a typo in a config file is invisible.

**Fix.** Delete or refresh `config.yaml`; load YAML strictly
(`yaml.Decoder.KnownFields(true)`) so unknown keys are an error.

---

## S3 — `tret server` ignores `config.yaml` unless `-config` is passed

**Fixed** (commit `c84f571`): the configuration of a directory is the
`config.yaml` in it.

`-config` defaults to `""` (`internal/config/config.go:68`), so a plain
`tret server` in a directory with a perfectly good `config.yaml` runs with
built-in defaults (`gpt-4o-mini` + `api.openai.com`). The README's quick start
implies the file *is* the configuration.

**Fix.** Default `-config` to `config.yaml` when the file exists.

---

## S3 — The whole process scope is copied into every job's input variables

**[measured]** The job record of the classifier example:

```json
"inputVariables": {
  "llm_request": { … },
  "request":     { … }        ← the parent variable, copied again
}
```

The engine copies the process scope into the job's local scope, and stores the
full input on the job record. If the agent transcript lives in a process
variable (the only recipe the current design offers), then **every turn
re-stores the whole transcript** — quadratic growth per instance — and the
worker output is stored a second time on the job record
(`docs/reference/bpmn/variable-mapping.md`: *"the full output variables
returned by the job worker are stored on the `Job` record"*).

This is the reason the transcript must move out of the engine
(`futurePlans.md` §3). It is also why the "budget as a BPMN counter"
recipe alone cannot scale.

---

## S3 — No observability at all

**Partly fixed** (commit `bcdb4c3`): every job is logged with its key, its
instance, its element, its attempt, its delivery token, its duration and its
outcome; every provider call with its model, its duration and its token
usage. The metrics, the traces, the health endpoints and `tret doctor` are
still open.

* `cmd/tret/server.go` logs with `fmt.Println`; there is no structured log,
  no job/instance/element correlation, no metric, no trace.
* The provider's error body is dropped (`pkg/llm/client.go:92` — deliberate,
  the unit test asserts it does not leak), so an incident says only
  `completion HTTP status 429` and the operator has to guess.
* The provider's `usage` (tokens, cost) is returned to BPMN but never
  aggregated anywhere. **Cost is invisible.**
* No `/healthz`, no `/readyz`, no `tret doctor`.
* `cmd/tret/server.go:67` throws away the `*zenclient.Worker` handle, so the
  server cannot even report its own subscription state.

**Fix.** `log/slog` with the job key / instance key / element id / delivery
token as fields; an OTel span per job (the engine already emits spans, so the
trace can be continued); Prometheus counters for jobs by type and outcome,
latency, in-flight, tokens and cost per profile; health and readiness
endpoints.

---

## S3 — The size of what a worker returns is unbounded

The HTTP response is capped at 8 MiB (`pkg/llm/client.go:51`), but the worker
returns the whole content, which the engine stores on the job record *and* in
the process variables. An 8 MiB model answer becomes an 8 MiB row.

**Fix.** Cap the returned payload (e.g. 256 KiB) with a distinct error code,
or spill to a store and return a reference (`futurePlans.md` §3.3).

---

## S3 — The JSON schema is compiled on every job

`pkg/llm/worker.go:156` builds a new `jsonschema.Compiler` (and recompiles
regexes) for every job. At a few hundred jobs/second this is pure waste.

**Fix.** Cache the compiled schema keyed by the hash of the raw schema.

---

## S4 — Duplicated worker plumbing

`strictDecode` is copy-pasted in `pkg/llm/worker.go:42` and
`pkg/shellexec/worker.go:53`; the "zero retries" error construction is
copy-pasted in four places (`pkg/llm/worker.go:37`, `pkg/shellexec/worker.go:48`,
`pkg/worker/registry.go:47`, `examples/classifier/report/report.go:39`).

**Fix.** One `pkg/contract` (or `internal/jobcontract`) with `DecodeStrict`,
the error taxonomy, and the result helpers — shared by the built-in workers,
the example worker and third parties.

---

## S4 — `pkg/llm` does not really reject unknown fields inside messages

`Message` is `map[string]any`, so `DisallowUnknownFields` does not apply
inside a message. `docs/workers.md` claims *"Unknown request fields are
rejected"* — true for the top level, false for message and tool bodies. The
extra fields are forwarded to the provider verbatim.

**Fix.** Either decode messages into a struct with the native fields
explicitly listed (`role`, `content`, `name`, `tool_calls`, `tool_call_id`,
`refusal`, …) or soften the documentation.

---

## S4 — The server exposes no concurrency control

**Partly fixed** (commit `5063e9e`): `max_active_jobs` is configurable. The
client-side semaphore is still open.

`cmd/tret/server.go:58-61` sets only the lock duration. `WithMaxActiveJobs`
exists in the zenclient but is unused, so the engine's default applies: **10
active jobs per job type per subscription** (`internal/config/config.go:179`).
One replica therefore tops out at 10 concurrent LLM calls, while the worker
side spawns an unbounded number of goroutines (`zenclient` starts one goroutine
per delivered job).

**Fix.** Make the cap a config value, set it explicitly, and add a client-side
semaphore so the worker refuses work it cannot take rather than queueing it in
memory.

---

## S4 — The worker client ID collides across hosts

**Fixed** (commit `5063e9e`): the client id is the host, the pid and a random
suffix, and it is configurable.

`cmd/tret/server.go:67` uses `fmt.Sprintf("tret-%d", os.Getpid())`. In
containers the PID is usually 1, so every replica registers as `tret-1`. The
engine keys its round-robin list by client ID
(`internal/cluster/jobmanager/server.go:986`), so two replicas with the same ID
collapse into **one** entry — one replica silently receives no jobs at all.
This is a hard blocker for horizontal scaling.

**Fix.** `clientID = hostname + "/" + pid + "/" + random-suffix`, exposed as a
config value with a sane default.

---

## S4 — The operator surface is too thin

**Fixed**: `tret status`, `instance`, `jobs`, `incidents`, `resolve`,
`complete`, `version` and `capabilities` read the engine over REST. A
`userTask` is a job like any other, and `tret complete` answers it from the
terminal.

`tret` could deploy, start and serve. There was no `status`, `instance`,
`jobs`, `incidents`, `resolve`, `complete`, `version`. The human-task client
(`TODO.md`) was the most visible gap — a `userTask` is a job like any other,
and the only way to answer it was the ZenBPM UI or `curl`.

**A bug the operator commands uncovered:** the engine sends no `state` for an
incident — an incident which was resolved carries the moment it was resolved
(`resolvedAt`). `internal/chat/session.go` compared the missing `state` with
`"resolved"`, so every incident of a chat ended the session, even a resolved
one. The check reads `resolvedAt` now.

---

## S4 — `internal/zen.Client` uses one 15 s timeout for everything

`internal/zen/client.go:19` — deploys of large BPMN files and instance reads
share a 15 s timeout with the fast calls. There is no retry, no backoff, and
no context parameter anywhere in the client.

---

## S4 — The house-keeping leftovers are still in the tree

`data/contexts/*.jsonl` and `state/` belong to the removed context store
(`TODO.md`, "Housekeeping"). `state/` is currently owned by root (created by
the compose volume mount).

---

## What is *not* wrong

To keep the list honest, these were checked and are fine:

* `internal/zen.Client.ListJobs` parses the flat `JobPage` shape
  (`items`/`page`/`size`/`totalCount`) that the engine actually returns —
  **verified against the live engine**. The partitioned shape is only used by
  the global `/v1/jobs` endpoint, which `tret` does not call.
* `GetInstance`, `ListIncidents`, `Deploy`, `StartInstance` shapes match the
  live engine; the live integration test passes.
* Input validation in both workers is strict, rejects unknown fields at the
  top level, caps sizes, and validates before any side effect.
* The provider body is never leaked into a BPMN error (asserted by a unit
  test).
* `pkg/shellexec` correctly drains both pipes, escalates `TERM`→`KILL`, kills
  the process group and does not wait for background descendants — all covered
  by unit tests.
* `examples/classifier` is a genuinely good end-to-end wiring example, and the
  live integration test is meaningful (it caught a real problem during this
  review).
