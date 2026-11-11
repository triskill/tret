# zenbpm-go — architecture, plan & progress tracker

Updated: 2026-07-15. Tick boxes as work lands. Every **D-number** is an architecture decision that
was discussed and agreed; changing one means editing this file first, then the code.

---

## 0. Shape of the thing

| Piece | What it is |
|---|---|
| **server** (`cmd/server`) | Deploys the agent-loop BPMN to ZenBPM at startup, registers gRPC job workers for every external task, runs the LLM `decide` step, runs tool execution, owns durable run state (SQLite). |
| **agent** (`cmd/agent`) | Workspace-facing CLI the human opens in a working directory: submits the goal, renders the conversation, answers engine `userTask`s with human input. |
| **engine** | `ghcr.io/pbinitiative/zenbpm:v1.9.0` — the state machine. Never re-implemented (D13). |
| **zenbpm-ui** | `ghcr.io/pbinitiative/zenbpm-ui:v1.9.0` on `:8081`, to inspect instances / tokens / jobs / incidents. |

## 1. Agreed decisions

| # | Topic | Decision |
|---|---|---|
| D1 | Process shape | One process `agent_loop`: `start → decide → exclusive GW → { execute → loop back · reply(userTask) → loop back · finish → endSuccess · fail → endFailure }` |
| D2 | Job transport | gRPC JobStream through the engine's own Go client `github.com/pbinitiative/zenbpm/pkg/zenclient` (`RegisterWorker`, locks, reconnect/backoff). REST only for deploy / start instance / readback |
| D3 | Who calls the LLM | The **server** runs `agent-decide`. The agent app never talks to a model |
| D4 | Human in the loop | The agent CLI *is* the `reply` user-task worker: it waits on the active user task and completes it with what the human typed |
| D5 | State ownership | Server owns durable state (SQLite under `--state-dir`: runs, decision trace, tool output spill). Agent keeps only local render state |
| D6 | Runner | Port `../executor/runner.py` semantics: `bash -lc`, own process group, TERM→KILL escalation, output caps, timeout, exit code preserved. Kills the `strings.Fields` bug |
| D7 | Tool surface | Exactly one tool, `bash`, max one call per decision, native OpenAI-style function calling |
| D8 | Recovery | No recovery matrix, no journal, no fenced generations. Unknown outcome → `fail` → accept the incident → stop |
| D9 | Packaging | Engine + UI in containers (pinned digest); `server` and `agent` as plain local binaries. Executor container tier replaced by local exec in the workspace |
| D10 | Diagram | Single hand-authored `bpmn/agent_loop.bpmn` **with BPMN DI** (`bpmndi:BPMNPlane`, shapes + waypointed edges) so zenbpm-ui renders it; embedded with `go:embed` |
| D11 | Retries | Per-node `zenbpm:taskDefinition` with `retries="3" retryBackoff="PT10S"` on the external tasks (engine default is a single attempt) |
| D12 | Loop guard | App-level budgets live in the server (max tool calls / model turns / wall clock; port JSON defaults 24 / 12 / 900s). Engine's 10 000-flow-node guard stays as backstop |
| D13 | Python tree | `../` stays a read-only behavioural reference: verified ZenBPM v1.9 adapter quirks, executor rules, tests to port. No cross imports; the Go tree stands alone |
| D14 | Route vocabulary | `{tool, message, finish, fail}` only — the Python `correct` route is dropped |

## 2. Engine facts this design leans on (checked in this machine's engine source)

Engine at `~/prg/zenbpm` == tag `v1.9.0`; docs at `~/prg/zenbpm/docs`.

- Deploy = `POST /v1/process-definitions`, body = raw BPMN bytes (`application/octet-stream`).
  Same normalized content ⇒ `200` with the existing `processDefinitionKey`; change ⇒ `201` + new key.
  Limit `httpServer.maxRequestBodyBytes` (10 MiB default).
- Start = `POST /v1/process-instances` with `{processDefinitionKey | bpmnProcessId, businessKey, variables}`
  ⇒ `201` with `{key, processDefinitionKey, state, version, variables}`. Lookups by `businessKey`.
- Jobs stream over gRPC `:9090` (`README` runs `-p 8080:8080 -p 9090:9090`). Client surface:
  `zenclient.NewGrpc(conn)`, `RegisterWorker(ctx, clientID, WorkerFunc, jobType...)`,
  `RegisterWorkerWithOptions` + `WithJobType/WithLockDuration/WithMaxActiveJobs`,
  `Worker.ExtendLock`, `WorkerError{ErrorCode, Variables, Retries, RetryBackoff}`.
  `WorkerFunc = func(ctx, *proto.WaitingJob) (map[string]any, *WorkerError)`.
- REST polling alternative (`docs/reference/jobs.md`): `GET /v1/jobs?state=active&jobType=…`,
  pagination is **per partition**, `retryAt` gates redelivery — i.e. an engine-side backoff a poller
  must not mistake for a new job. Kept only for the agent's user-task lookup if we avoid gRPC there.
- Completion is idempotent (`201` again), a terminated/failed job answers `409`; without an
  `errorCode` a failure spends one retry, and only the attempt that leaves zero retries makes an
  incident. Lock default 30 s, `max_active_jobs` default 10, per leader — a multi-leader cluster
  multiplies the cap, which pushes us to a **single-partition local engine**.
- A `bpmn:userTask` yields an ordinary externally-handled job ("User Tasks are handled as Service
  Tasks") — this is what makes D4 legal.
- `zenbpm:ioMapping` (`<zenbpm:output source="*">$.route</zenbpm:output>`) publishes job result
  variables into the process scope; default `variableUpdate` type is moved **down** to child scopes.
- `zenbpm:taskHeaders` are delivered verbatim with every job of an element — the right place for
  per-node hints (e.g. which model/purpose), kept out of process variables.
- Locks live on the partition leader: a leader change forgets all locks and redelivers open jobs,
  so handlers must tolerate a second delivery while still working → D8.
- Incident `message` carries the reason (`"<error> (job <key>: 3 attempts, retries exhausted)"`).

## 3. Known-broken baseline (clear this first)

- [x] `go build ./... && go vet ./...` fail: `cmd/orchestrator/main.go:11:6: main redeclared` (vs `main_dispatch.go`)
- [x] `cmd/orchestrator/` is superseded by `cmd/server` (D3) — removed
- [x] `go.mod` module path settled to `github.com/pbinitiative/zenbpm-go`
- [ ] `bpmn/agent_loop.bpmn` has **no `zenbpm:taskDefinition`/ioMapping and no `bpmndi` block** ⇒ jobs get no type and zenbpm-ui shows nothing (D10)
- [ ] `internal/api/client.go` `FetchInstruction` returns a hardcoded instruction — replace with real engine calls
- [x] `internal/orchestrator/deployer.go` POSTs BPMN bytes via zen client and returns key
- [x] `internal/executor/runner.go` ported to bash -lc process group runner (D6)
- [ ] `internal/job_workers/dispatcher.go` mixes routing with `json.Unmarshal` of raw text, and `internal/brain/llm.go` parses decisions by substring → structured tool calls (P4)
- [ ] `internal/brain/*` only knows Ollama + `MockLLM` → OPEN-4
- [ ] repo-root `session.json` is stale REPL chat data → stop committing; state belongs to the server (D5)
## 4. Work packages

### P0 — honest skeleton (nothing pretending)
- [ ] `cmd/server`, `cmd/agent` each own `main`; shared code under `internal/`
- [ ] `internal/config`: engine REST URL, engine gRPC URL, state dir, workspace dir, model+key, budgets, per-node timeouts — flags + env (+ optional YAML)
- [ ] `compose.yaml`: engine + UI, **pinned by image digest**, ports 8080 / 9090 / 8081, volume for state, engine config with `jobManager` overrides and single partition
- [ ] `Makefile`: `up`, `down`, `build`, `test`, `run-agent`, `deploy`
- [ ] delete/docs: README explaining the two-app split + status

### P1 — engine adapter (`internal/zen`)
- [ ] deploy BPMN bytes → parse `processDefinitionKey`, treat 200/201 both as success
- [x] start instance with `businessKey` = run id, initial variables (`goal`, `route`)
- [ ] readback helpers: instances by businessKey, jobs of a PI, incidents, history
- [ ] typed errors for the three documented outcomes (✅ result / ✅ X / ❌ outcome + retry) so callers can stop safely (Q5)

### P2 — the process definition (`bpmn/agent_loop.bpmn`)
- [ ] serviceTasks `Task_Decide` (`agent-decide`) and `Task_Execute` (`agent-execute`) with `zenbpm:taskDefinition` + `retries=3 retryBackoff=PT10S` (D11)
- [ ] `Task_Reply` as `bpmn:userTask` (job type per D4), `Task_End`
- [ ] exclusive gateway conditions matching D14 route names
- [ ] `<bpmn:extensionElements>` ioMapping publishing `route` (+ `result`, `assistant_text`), taskHeaders for node hints
- [ ] hand-authored BPMN DI verified by opening the file in `zenbpm-bpmn-js` Modeler (D10)

### P3 — executor (D6)
- [ ] port `../executor/runner.py`: `bash -lc`, own process group, TERM→KILL, `timeout`, stdout/stderr caps, exit code retention, `before = now()` order
- [ ] large output spill (file on disk + a bounded summary and `<output_ref>`) — keep small, engine variables stay small
- [ ] ID-per-attempt so a redelivered job swaps both the active lock and evidence (keep, not overwrite)

### P4 — decide worker (the brain, D3)
- [ ] `openai_compat.go`: OpenAI-compatible chat + function calling, single `bash` tool, SSE streaming, ≤100 messages / ~40k token window, `zen-turn-id`/`zen-run-id` headers
- [ ] `prompts.go`: workspace SYSTEM prompt (+ copy `../harness/authoring/system_agent.txt` for reference), refusal/`finish` rules
- [ ] deterministic route classification: native `tool_calls` / `finish`/`reflect` objects, never `strings.Contains`
- [ ] result → job variables `{route, command|message}` and `{route:"finish"|"fail"}`
- [ ] `MockLLM` behind an interface for tests only, never a silent default

### P5 — agent CLI
- [ ] watch this run's active `reply` user task and complete it with human input
- [ ] render conversation + one-liner per decision/tool/incident
- [ ] flags: `-goal`, `-continue`, `--workspace`, `--server`
- [ ] stop cleanly on `endSuccess` / `endFailure` / incident

### P6 — server state & limits
- [ ] SQLite at `--state-dir`: runs, decision trace, tool output spill files
- [ ] tool output spill to disk, only a short summary goes to engine
- [ ] budgets (D12) enforced server-side; route `fail` connects fail-end detail
- [ ] restart = deploy is idempotency safe and runs readable from db

### P7 — tests
- [ ] go unit tests for zen client wrapper
- [ ] integration tests against engine containers

