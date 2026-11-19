# tret — architecture & open work

Updated: 2026-11-19 (commit f540cde). The platform is: generic versioned job
workers (`llm-call-v1`, `exec-command-v1`; `pkg/llm`, `pkg/shellexec`,
`pkg/worker`), deploy/start/readback commands (`internal/zen`, `cmd/tret`),
and the ZenBPM engine as the routing state machine. A loop is a BPMN file:
logic, budgets, and routing live in the process via gateways and process
variables, not in compiled code. The former bundled `agent_loop` process,
`internal/brain` route vocabulary, `internal/server` agent workers and
transcript store, and the interactive `tret agent` CLI were removed
(`git show HEAD~1:bpmn/agent_loop.bpmn` has the old definition).

## Verified current state

- Workers the server registers: `llm-call-v1`
  (`pkg/llm`, JobType constant), `exec-command-v1` (`pkg/shellexec`).
  `-workers` selects which ones subscribe; default is both.
- `exec-command-v1` limits: 32 KiB per stream (`pkg/shellexec/runner.go`
  `outputLimit`), per-job timeout, TERM->KILL.
- Live integration tests gated on `TRET_INTEGRATION=1`
  (`internal/integration/toolkit_test.go`).
- No server-side agent/budget/state code remains in `cmd/`, `pkg/`,
  `internal/` (grepped).

## Done

- `deploy`/`start` with exact definition keys (`cmd/tret/process.go`).
- Explicit worker registry (`pkg/worker`); selectable subscriptions.
- `llm-call-v1`: named profiles, tools, schema-validated `structured_output`
  (contract in docs/workers.md).
- `exec-command-v1` from the old executor; side-effect/replay rules in
  docs/workers.md.
- Unit tests; live `TRET_INTEGRATION=1` test for the toolkit path.

## Open

- [ ] Rebuild the coding-agent loop as `examples/agent-loop/`: a deployable
      BPMN using only `llm-call-v1`, `exec-command-v1`, and a `userTask` for
      human input. Old loop for reference:
      `git show HEAD~1:bpmn/agent_loop.bpmn` (+ its integration test, which
      also holds the `awaitCompleted` polling pattern to reuse). Translate
      `agent-execute` to `exec_request`; counters (`model_turns`,
      `tool_calls`, `budget_allowed`) become process variables updated via
      gateway/loop-back instead of a server-side budget worker. Needs a live
      integration test mirroring toolkit_test.go. First run `make up` and a
      real engine; do not verify against mocks only.
- [ ] Document the run-budget-as-BPMN recipe (counter variable + exclusive
      gateway + loop-back) in docs/ once the agent-loop example proves it.
- [ ] Generic human-task client: answer `userTask` jobs from the terminal
      using `internal/zen` `ListJobs`/`CompleteJob` (agent-loop-agnostic).
- [ ] Exactly-once / crash recovery design for non-idempotent jobs (LLM
      billing, shell side effects): journal or fencing keyed on engine job
      key. Design note first; docs/workers.md already states at-least-once.
- [ ] Output spill: route >32 KiB exec output / long LLM content to storage
      with a bounded summary variable.
- [ ] `http-call-v1` worker — deliberately deferred until the agent-loop
      example shows a real need.

## Housekeeping

- Untracked leftovers from the removed context store exist on dev machines:
  `data/contexts/`, `state/` (both gitignored). Safe to delete locally.
