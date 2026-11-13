# tret

[![CI](https://github.com/triskill/tret/actions/workflows/ci.yaml/badge.svg)](https://github.com/triskill/tret/actions/workflows/ci.yaml)

An AI agent whose control loop is an **executable BPMN process**, not hidden
control flow. [ZenBPM](https://github.com/pbinitiative/zenbpm) (the workflow
engine) creates every job and routes every turn; tret supplies the LLM
decision worker, durable run state, and the human-facing CLI.

## Topology

```
┌───────────────┐     REST (deploy/start/readback)    ┌──────────────┐
│ tret server   │ ◀─────────────────────────────────▶ │ zenbpm       │
│               │ ◀───────── gRPC JobStream ───────── │ engine       │
│ · decide      │                                     └─────▲────────┘
│ · fail report │                                           │ REST
│ · context     │                                     ┌─────┴────────┐
│   state       │                                     │ tret agent   │
└───────────────┘                                     │ (your CLI)   │
                                                      └──────────────┘
```

- **`tret server`** subcommand deploys the embedded `agent_loop` process at
  startup (idempotent), registers gRPC job workers (`agent-decide`,
  `agent-fail-report`), calls the LLM, and owns durable context state under
  `--state-dir`.
- **`tret agent`** subcommand (the default, so bare `tret` works) is what you
  run in a working directory: it starts a run (or attaches with `-continue`)
  and completes the process's `agent-reply` user tasks with what you type. It
  never talks to a model.
- **`zenbpm-ui`** on <http://localhost:8081> lets you inspect instances,
  tokens, jobs, and incidents.

## Quick start

Requires Go (version in `go.mod`), Docker with Compose, and either a local
Ollama or an OpenAI-compatible API key.

```sh
git clone git@github.com:triskill/tret.git && cd tret
make up                                  # engine + UI containers
cp config.example.yaml config.yaml       # edit model / api_key / api_base
make run-server CONFIG=config.yaml       # terminal 1: deploys + serves workers
make run-agent GOAL="Say hi"             # terminal 2: chat with the run
```

In the agent prompt, `/exit` cancels the run. Re-attach later with
`make run-agent ARGS="-continue run-<businessKey>"`.

No model at hand? `make e2e` runs the whole stack against a mock LLM that
immediately finishes — a smoke test of engine wiring, not of intelligence.

## Install

Make targets are just conveniences around one static binary; install it and
run from any folder (the engine must be reachable):

```sh
go install github.com/triskill/tret/cmd/tret@latest

tret server -config config.yaml    # worker daemon, one per engine
tret -goal "work on this repo"     # agent CLI; bare 'tret' == 'tret agent'
```

## Configuration

`config.yaml` — see [config.example.yaml](config.example.yaml) for all keys.
Server flags use dashes (`state_dir` → `-state-dir`); `wall_clock_sec`
uses `-wall-clock`. Highlights:

| key | meaning |
|---|---|
| `api_key` + `api_base` | non-empty key ⇒ OpenAI-compatible chat endpoint |
| `ollama_base` | empty key ⇒ local Ollama |
| `engine_rest` / `engine_grpc` | where the engine lives (`:8080` / `:9090`) |
| `state_dir` | server's durable context store |
| `max_tool_calls` / `max_model_turns` / `wall_clock_sec` | positive run budgets (24 / 12 / 900 by default) |

## Status

Early development. BPMN owns every transition, including budget checks before
model and tool calls. Counters and the run start time are persisted as process
variables; the server evaluates limits and BPMN routes exhaustion to failure.
The wall-clock budget starts at the first model-budget check and is checked at
step boundaries, not while waiting for human input. Completed instances carry
`run_outcome: success|failure` because both BPMN end events complete normally.

Model and command jobs have one attempt to avoid automatic retries of side
effects. Engine redelivery is still possible; execution is not exactly-once.
Conversation state is JSONL; SQLite, output spill, and context-window bounds
remain unfinished. The executor runs commands locally with the server's
permissions; it is not a security sandbox. `agent -workspace` does not select
the execution directory: configure the server's workspace instead.
See [TODO.md](TODO.md) for remaining work.

Live process tests (requires the Compose engine):

```sh
make up
TRET_INTEGRATION=1 go test -v ./internal/integration
```

## License

[GPL-3.0](LICENSE)
