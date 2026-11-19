# tret — architecture & open work

Updated: 2026-11-19. The platform is: generic versioned job workers
(`llm-call-v1`, `exec-command-v1`; `pkg/llm`, `pkg/shellexec`, `pkg/worker`),
deploy/start/readback commands (`internal/zen`, `cmd/tret`), and the ZenBPM
engine as the routing state machine. A loop is a BPMN file: logic, budgets,
and routing live in the process via gateways and process variables, not in
compiled code. The former bundled `agent_loop` process, `internal/brain`
route vocabulary, `internal/server` agent workers and transcript store, and
the interactive `tret agent` CLI were removed; git history keeps them for
reference. Rebuilding a coding-agent loop means deploying a BPMN file that
uses the generic workers plus any app-owned workers.

## Done

- Independent deploy/start with exact definition keys.
- Explicit worker registry; selectable worker subscriptions in `tret server`.
- `llm-call-v1`: named profiles, tools, schema-validated structured output.
- `exec-command-v1` from the old executor: capped output, TERM→KILL, per-job
  timeout; side-effect/replay rules documented in docs/workers.md.
- Unit tests; live `TRET_INTEGRATION=1` tests for the toolkit path.

## Open

- [ ] Exactly-once execution / crash recovery: journal or fencing for
      non-idempotent jobs (LLM billing, shell side effects).
- [ ] Output spill: > 32 KiB exec output and long LLM content should move to
      storage with a bounded summary variable.
- [ ] Express run budgets as a BPMN recipe (counter tasks + gateways) and
      document the pattern; remove any lingering notion of server-side limits.
- [ ] Generic human-task client (an interactive `userTask` answerer, not tied
      to any process shape).
- [ ] Rebuild the coding-agent loop as an example BPMN on generic workers.
- [ ] HTTP call worker (`http-call-v1`) once patterns around it stabilize.
