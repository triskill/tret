# tret

An AI agent whose control loop is an **executable BPMN process**, not hidden
control flow. [ZenBPM](https://github.com/pbinitiative/zenbpm) (the workflow
engine) creates every job and routes every turn; tret supplies the LLM
decision worker, tool execution, durable run state, and the human-facing CLI.

## Topology

```
┌─────────────┐   REST (deploy/start/readback)   ┌─────────────┐
│ tret server │ ◀──────────────────────────────▶ │ zenbpm      │
│             │ ◀──────── gRPC JobStream ─────── │ engine      │
│  · decides  │                                  └──────▲──────┘
│  · executes │                                         │ REST
│  · owns     │                                  ┌──────┴──────┐
│    state    │                                  │ tret agent  │
└─────────────┘                                  │ (your CLI)  │
                                                 └─────────────┘
```

- **`tret server`** (`cmd/server`) deploys the embedded `agent_loop` process at
  startup (idempotent), registers gRPC job workers (`agent-decide`,
  `agent-fail-report`), calls the LLM, and owns durable context state under
  `--state-dir`.
- **`tret agent`** (`cmd/agent`) is what you run in a working directory: it
  starts a run (or attaches with `-continue`) and completes the process's
  `agent-reply` user tasks with what you type. It never talks to a model.
- **`zenbpm-ui`** on <http://localhost:8081> lets you inspect instances,
  tokens, jobs, and incidents.

## Quick start

Requires Go (version in `go.mod`), Docker with Compose, and either a local
Ollama or an OpenAI-compatible API key.

```sh
make up                                  # engine + UI containers
cp config.example.yaml config.yaml       # edit model / api_key / api_base
make run-server CONFIG=config.yaml       # terminal 1: deploys + serves workers
make run-agent GOAL="Say hi"             # terminal 2: chat with the run
```

In the agent prompt, `/exit` cancels the run. Re-attach later with
`make run-agent ARGS="-continue run-<businessKey>"`.

No model at hand? `make e2e` runs the whole stack against a mock LLM that
immediately finishes — a smoke test of engine wiring, not of intelligence.

## Configuration

`config.yaml` (or flags of the same names) — see
[config.example.yaml](config.example.yaml) for all keys. Highlights:

| key | meaning |
|---|---|
| `api_key` + `api_base` | non-empty key ⇒ OpenAI-compatible chat endpoint |
| `ollama_base` | empty key ⇒ local Ollama |
| `engine_rest` / `engine_grpc` | where the engine lives (`:8080` / `:9090`) |
| `state_dir` | server's durable context store |
| `max_tool_calls` / `max_model_turns` / `wall_clock_sec` | run budgets |

## Status

Early. What works today: deploy → start → decide → reply loop with durable
per-run context, fail routing with incident reporting, mock-LLM e2e. What is
deliberately missing: the `bash` tool branch of the process (the executor is
implemented and unit-tested, the BPMN branch is not yet), structured
`tool_calls`, budgets enforcement, SQLite spill. The full decision record
(D1–D14) and work packages (P0–P7) live in [TODO.md](TODO.md).

## License

[MIT](LICENSE)
