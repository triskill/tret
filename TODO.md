# tret — architecture & open work

Updated: 2026-11-20 (commit `0cb28f6`). The platform is: generic versioned job
workers (`llm-call-v1`, `exec-command-v1`; `pkg/llm`, `pkg/shellexec`,
`pkg/worker`), deploy/start/readback commands (`internal/zen`, `cmd/tret`),
and the ZenBPM engine as the routing state machine. A loop is a BPMN file:
logic, budgets, and routing live in the process via gateways and process
variables, not in compiled code. The former bundled `agent_loop` process,
`internal/brain` route vocabulary, `internal/server` agent workers and
transcript store, and the interactive `tret agent` CLI were removed
(`git show HEAD~1:bpmn/agent_loop.bpmn` has the old definition).

**Two documents govern the work below and hold the detail:**

- [`docs/mistakesToFix.md`](docs/mistakesToFix.md) — the code review: 24
  findings, each reproduced against a live engine or traced to the engine
  source. Read it before changing anything in `pkg/` or `cmd/`.
- [`docs/futurePlans.md`](docs/futurePlans.md) — the architecture plan: 8
  phases, from the correctness foundation to the reference agent loop.

This file is the *actionable* list. Everything else lives in those two.

## Verified current state

- Workers the server registers: `llm-call-v1`
  (`pkg/llm`, JobType constant), `exec-command-v1` (`pkg/shellexec`).
  `-workers` selects which ones subscribe; default is both.
- `exec-command-v1` limits: 32 KiB per stream (`pkg/shellexec/runner.go`
  `outputLimit`), per-job timeout, TERM->KILL.
- Live integration tests gated on `TRET_INTEGRATION=1`
  (`internal/integration/toolkit_test.go`); green against ZenBPM v1.9.0.
- No server-side agent/budget/state code remains in `cmd/`, `pkg/`,
  `internal/` (grepped).
- Measured against the live engine (2026-11-20), recorded in the review:
  default job lock 30 s; engine default `retries` 1; `retries` is *not*
  consulted when a worker failure carries an error code; the whole process
  scope is copied into every job's input variables; a BPMN output mapping
  (`=turn + 1`) is a working pure-BPMN counter; the engine rejects
  `scriptTask` but supports timer events, multi-instance, DMN and FEEL.

## Done

- `deploy`/`start` with exact definition keys (`cmd/tret/process.go`).
- Explicit worker registry (`pkg/worker`); selectable subscriptions.
- `llm-call-v1`: named profiles, tools, schema-validated `structured_output`
  (contract in docs/workers.md).
- `exec-command-v1` from the old executor; side-effect/replay rules in
  docs/workers.md.
- Unit tests; live `TRET_INTEGRATION=1` test for the toolkit path.
- Code review with live-engine evidence (`docs/mistakesToFix.md`) and a
  phased architecture plan (`docs/futurePlans.md`).

## Open — Phase 0: correctness (blocking everything else)

Do these before any new capability, any scaling work, and any agent-loop
example. Each item cites its finding in `docs/mistakesToFix.md`.

- [x] **Detach the side effect from the job stream.** `pkg/llm/worker.go:202`
      and `pkg/shellexec/worker.go:91` used the gRPC stream context; a
      reconnect cancelled the running call/command and the job was
      redelivered. Now `worker.Detach` gives the work its own deadline.
      (S1)
- [x] **Lock discipline.** One registration helper that always sets a lock
      duration; derive it from the worker's worst-case runtime; renew with
      `ExtendDeliveryLock`; validate at startup. The engine default (30 s) is
      shorter than tret's own 120 s budget, so a slow model was executed
      several times over. (S1)
      **Done**: `pkg/worker.Spec` refuses a lock which does not outlast the
      declared runtime, `worker.Subscribe` is the safe path, the server
      validates the configured lock. The renewal with `ExtendDeliveryLock`
      is still open.
- [x] **Idempotency journal** keyed on the **engine job key** (never on
      attempt or delivery token — the engine docs say so explicitly). Look up
      → claim → execute → store the result; a redelivery returns the
      memoized result. Plan: `futurePlans.md` §2.1. (S1)
      **Done**: `pkg/journal` with a file store (one node) and an in-memory
      store; both built-in workers use it when `journal_dir` is set. The
      claim of a step is waited for instead of failed, every change of a
      claim is serialized by an OS file lock, the lease must outlast the
      work, and the budget of a job starts with the work. A store several
      nodes can reach is still open.
- [x] **Error taxonomy.** Every failure carried an error code, so the
      engine never spent a retry and the BPMN `retries` attribute was dead.
      Now: invalid request / invalid output -> BPMN error; transient
      (429, 5xx, dial timeout) -> no error code, the engine retries with
      `retryBackoff`. (S2)
      Verified against the live engine: a provider which answers 429 twice
      and then succeeds runs three times and the instance completes.
- [x] **Config validation at startup.** An invalid server profile was
      reported as `invalid_input` (the process's fault) and
      `internal/config.FromFlags` called `os.Exit`. Now the profiles are
      validated at boot and the configuration is reported as an error.
      (S2)
      Still open: the "no api key -> Ollama" heuristic surprises.
- [ ] **`exec-command-v1` hardening.** Per-job workspace instead of the
      shared `exec_root`, minimal env. (S2)
      **Done**: the shell reads no profile file, so the environment of a
      command does not depend on the files in the workspace; with
      `exec_workspace: instance` every instance gets a directory of its own
      under the `exec_root`, so the instances do not write into each other's
      files. The directories are not cleaned up, and a sandbox is still
      open.

## Open — Phase 1: state outside the engine

- [ ] **Artifact store** (content-addressed; local fs + S3) for large exec
      output and long model content; the worker returns a reference plus a
      preview. `futurePlans.md` §3.1, §3.3.
- [ ] **Transcript store.** The process scope is copied into every job's
      input variables, so a transcript kept in a variable grows
      quadratically and is duplicated per job. Keep the transcript outside
      the engine; the variable holds `{transcript_id, turn, digest}`.
      `futurePlans.md` §3.2.
- [ ] **Output envelope.** Common result shape (`result`, `usage`,
      `artifacts`, `cost`) across workers; cap what a worker returns (today
      an 8 MiB model answer becomes an 8 MiB engine row). `futurePlans.md`
      §3.4.

## Open — Phase 2: budgets

- [ ] **Document the run-budget-as-BPMN recipe.** Verified mechanism: an
      output mapping (`=turn + 1` -> `turn`) increments the counter on every
      completion, an exclusive gateway on `turn < max` closes the loop. No
      script task — the engine rejects `scriptTask`. `futurePlans.md` §4.1.
- [ ] **Server-side spend guard.** A BPMN counter cannot cap money, because
      token usage is only known after the call. Consult a budget ledger
      *before* the call and fail with a catchable `budget_exceeded`.
      `futurePlans.md` §4.2.

## Open — Phase 3: horizontal scale

- [x] **Unique client ID.** `tret-<pid>` collides across containers (PID 1),
      which collapses replicas into one round-robin entry — one replica
      silently gets no jobs. (S4)
      **Done**: host + pid + a random suffix, configurable with `client_id`.
- [ ] **Backpressure.** Set `WithMaxActiveJobs` explicitly (engine default:
      10 per subscription) and add a client-side semaphore.
      **Done**: `max_active_jobs` is configurable. The client-side semaphore
      is still open.
- [ ] **Health/readiness** endpoints and `tret doctor`.
- [ ] **Per-job workspace / shared store** so replicas are interchangeable;
      secrets from a secret store, not a config file.
      Plan: `futurePlans.md` §5.

## Open — Phase 4: observability & operator surface

- [ ] **Structured logs** (`log/slog`) with job key, instance key, element
      id, delivery token, attempt, worker id. Today: `fmt.Println`.
      **Done**: every job is logged with its key, instance, element, attempt,
      delivery token, duration and outcome; every provider call with its
      model, duration and token usage. The level is configurable.
- [ ] **Metrics.** Jobs by type/outcome, latency, in-flight, tokens and cost
      per profile, provider errors, and `tret_journal_hits_total` — the
      idempotency effectiveness metric.
- [ ] **Tracing.** The engine does not propagate trace context into job
      headers; link spans on the job key until it does.
- [ ] **CLI.** `tret status`, `instance`, `jobs`, `incidents`, `resolve`,
      `version`, `capabilities`. `futurePlans.md` §6.4.

## Open — Phase 5: worker contracts

- [ ] **Generic human-task client**: answer `userTask` jobs from the
      terminal using `internal/zen` `ListJobs`/`CompleteJob`
      (agent-loop-agnostic). A `userTask` is an ordinary job
      (`user-task-type` by default).
- [ ] **Machine-readable contract manifest** per job type (input/output
      schema, error codes and their retry class, side-effect class, max
      runtime, idempotency key) + a BPMN linter that checks a process
      against it. `futurePlans.md` §7.1.
- [ ] **Shared contract test suite** (`pkg/workertest`), including
      *idempotent under redelivery*, so a new worker does not reinvent
      validation, error codes and tests. `futurePlans.md` §7.2.
- [ ] **Decouple workers from the engine API.** `pkg/worker.Registry`
      exposes `proto.WaitingJob` and `*zenclient.WorkerError`; introduce
      `pkg/contract` plus one adapter. `futurePlans.md` §7.3.
- [ ] **Tool-routing guide.** The engine's job type is a static attribute, so
      a dynamic tool set needs either one task per tool or a narrow dispatch
      worker. Document the trade-off. `futurePlans.md` §7.4.
- [ ] `http-call-v1` worker — still deliberately deferred until the
      agent-loop example shows a real need.

## Open — Phase 6: security

- [ ] **mTLS to the engine**; today `insecure.NewCredentials()`
      (`internal/zen/grpc.go:13`) — loopback only.
- [ ] **Sandboxed `exec-command-v1`** (container per job: no network by
      default, CPU/memory/pid limits, read-only rootfs) or an explicit,
      loudly warned trusted-single-tenant mode. A loop that feeds model output
      into a shell is RCE by construction. `futurePlans.md` §8.

## Open — reference agent loop

- [ ] **`examples/agent-loop/`**: a deployable BPMN using only `llm-call-v1`,
      `exec-command-v1` and a `userTask`. Old loop for reference:
      `git show HEAD~1:bpmn/agent_loop.bpmn` (+ its integration test, which
      also holds the `awaitCompleted` polling pattern to reuse). Translate
      `agent-execute` to `exec_request`; counters become process variables
      (verified mechanism: an output mapping, see Phase 2). Needs a live
      integration test mirroring `toolkit_test.go`. First run `make up` and a
      real engine; do not verify against mocks only.
- [ ] **BPMN authoring guide**: the recipes (counter, loop-back, error
      boundary, tool routing) with copy-pasteable snippets and the engine
      idioms (FEEL, DMN, timer events, multi-instance).
- [ ] `tret new worker <type>` scaffolding: worker skeleton + manifest +
      contract tests.

## Housekeeping

- [ ] Refresh or delete the stale local `config.yaml` (still lists the
      removed `agent-budget` workers; `tret server -config config.yaml`
      fails immediately). Make the YAML loader strict so unknown keys are an
      error.
      **Done**: the loader is strict, so the stale keys are reported with
      their lines; the local file is refreshed.
- [x] `tret server` should pick up `config.yaml` by default when it exists.
- [ ] Untracked leftovers from the removed context store exist on dev
      machines: `data/contexts/`, `state/` (both gitignored). Safe to delete
      locally.
- [ ] Cache the compiled JSON schema per job instead of recompiling it for
      every `llm-call-v1` job.
