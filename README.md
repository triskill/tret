# tret

[![CI](https://github.com/triskill/tret/actions/workflows/ci.yaml/badge.svg)](https://github.com/triskill/tret/actions/workflows/ci.yaml)

A small platform for **BPMN-driven agent loops**.
[ZenBPM](https://github.com/pbinitiative/zenbpm) owns scheduling and routing;
tret deploys your BPMN files and hosts **generic job workers** — one-call LLM
completions and shell commands — that any process can reference. A new loop is
just a BPMN file: create or edit it, deploy it, done. No new Go code until you
need a genuinely new capability.

## How a loop works

```
 Your process.bpmn ──tret deploy──▶ zenbpm engine
                                       │ gRPC jobs (jobType per task)
                                       ▼
 tret server ── llm-call-v1 ──▶ OpenAI-compatible endpoint
              ── exec-command-v1 ─▶ bash -lc in the server's exec root
 app worker (your code) ──▶ anything else your process references
```

- **`tret server`** (the default subcommand) registers the selected generic
  workers over gRPC and streams jobs. It deploys nothing itself.
- **`tret deploy -file process.bpmn`** prints the definition key.
- **`tret start -process KEY_OR_ID -variables vars.json`** starts an instance.
  `-definition` pins an exact version; `-business-key` is accepted.
- Your own workers register directly against the engine (see the
  [classifier example](examples/classifier/README.md)); the core does not
  need to know their job types.

Process-level logic — loops, budgets, routing on model output — belongs in the
BPMN file (gateways and process variables), not in trev code. Human
`userTask`s surface as ordinary jobs on the same engine API.

## Generic workers

`llm-call-v1` makes one model call: optional native tools, JSON-schema-validated
`structured_output`, named server-side model profiles so credentials never
travel in process variables. No hidden loop, no conversation state, no tool
execution inside the worker.

`exec-command-v1` runs one `bash -lc` command per job in the configured
`exec_root` with capped output and a bounded timeout. Exit codes and timeouts
are ordinary results for the process to route on; this is a tool, not a
sandbox, and jobs are at-least-once — see the contract docs.

Only the job types you select are registered:

```sh
tret server -config config.yaml                  # llm-call-v1 + exec-command-v1
tret server -workers llm-call-v1                 # LLM only
```

See [worker contracts](docs/workers.md) for exact job variables, error codes,
limits, delivery semantics, and how to add a worker.

## Quick start

Requires Go (version in `go.mod`), Docker with Compose, and either a local
Ollama or an OpenAI-compatible API key.

```sh
git clone git@github.com:triskill/tret.git && cd tret
make up                                  # engine + UI containers
cp config.example.yaml config.yaml       # edit model / api_key / api_base
make run-server                          # terminal 1: serves generic workers
tret deploy -file examples/classifier/classifier.bpmn
tret start -process request_classifier -variables examples/classifier/variables.json
```

The [classifier example](examples/classifier/README.md) is a complete runnable
loop — one LLM classification, an application-owned report worker, gateway
routing — and shows a separate worker process alongside the platform.

Tests run against a live engine with `make up` plus `TRET_INTEGRATION=1 go test
./internal/integration`; unit tests run everywhere. Real-model acceptance is
opt-in per the example READMEs.

## Status

Early development. Nothing is deployed or executed automatically; side effects
happen only when a deployed process schedules a job. Engine job redelivery and
crash-recovery limits are documented in [docs/workers.md](docs/workers.md) and
still open, as are per-process variables budgets expressed as BPMN patterns and
output spill (see TODO.md). The former bundled `agent_loop` process, its
`agent-*` workers, transcript store, and interactive `tret agent` CLI were
removed (see git history); rebuild such loops as plain BPMN plus these generic
workers.

## License

[GPL-3.0](LICENSE)
