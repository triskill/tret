# tret

[![CI](https://github.com/triskill/tret/actions/workflows/ci.yaml/badge.svg)](https://github.com/triskill/tret/actions/workflows/ci.yaml)

A small platform for **BPMN-driven agent loops**.
[ZenBPM](https://github.com/pbinitiative/zenbpm) owns scheduling and routing;
tret deploys your BPMN files and hosts **generic job workers** — one-call LLM
completions and shell commands — that any process can reference. A new loop is
just a BPMN file: create or edit it, deploy it, done. No new Go code until you
need a genuinely new capability.

## Terminal chat

Install the command once, start the engine, then run `tret` in your project:

```sh
make install                         # installs into $(go env GOPATH)/bin
make up                              # run from this repository
# Ensure $(go env GOPATH)/bin is on PATH.
cd /path/to/your/project
tret -config /absolute/path/to/config.yaml
```

With a `config.yaml` in the current directory, just run **`tret`**. Use
`tret chat` for the same command. Configure the model as in
`config.example.yaml`; it must support OpenAI-compatible JSON schema output.
The engine must already be running; chat does not start Docker automatically.
Use a trusted local engine: anyone with engine access can inspect prompts,
complete approval jobs, or start the deployed process. Approval is a terminal
interaction, not an engine authorization boundary. The bundled Compose setup
publishes the engine and UI on loopback only.

Chat deploys an embedded [BPMN loop](internal/chatflow/chat.bpmn) and hosts
its workers for that session. The model proposes one shell command or a final
answer. Commands are shown as quoted strings so newlines and control
characters are visible. Enter `y` or `yes` to approve (case-insensitive), or
press Enter to decline. Commands run in the **current directory**, regardless
of `exec_root` or `exec_workspace` in the server config. They can read and change files with
**your permissions: there is no sandbox**. Inspect commands before approving.
The shell has a minimal environment; use explicit paths for project virtual
environments or tools outside its PATH.

BPMN owns routing and limits each user message to **eight model calls**.
`/exit` or EOF ends the session. Ctrl-C cancels the process, but an already
approved command may finish while chat waits to record its outcome. Command
results are journaled in `.tret/journal` by default (`journal_dir` overrides
it); do not remove active journal files. Session-specific worker types prevent
other running servers from executing chat commands in another workspace.

This is a small local testing assistant, not an unattended or resumable agent.
The transcript is bounded to 128 KiB and copied through process variables;
large transcripts/artifact storage remain open work. The engine retains
process records, including prompts and command output: do not send secrets.
No new Go code is needed to inspect or edit the loop itself.

## How a loop works

```
 Your process.bpmn ──tret deploy──▶ zenbpm engine
                                       │ gRPC jobs (jobType per task)
                                       ▼
 tret server ── llm-call-v1 ──▶ OpenAI-compatible endpoint
              ── exec-command-v1 ─▶ shell in the server's exec root
 app worker (your code) ──▶ anything else your process references
```

- **`tret`** starts terminal chat with an embedded reference BPMN loop.
- **`tret server`** registers the selected generic
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

`exec-command-v1` runs one `bash --noprofile --norc -c` command per job in the configured
`exec_root` with capped output and a bounded timeout. Exit codes and timeouts
are ordinary results for the process to route on; this is a tool, not a
sandbox, and jobs are at-least-once — see the contract docs.

Only the job types you select are registered:

```sh
tret server -config config.yaml                  # llm-call-v1 + exec-command-v1
tret server -workers llm-call-v1                 # LLM only
```

Every subscription asks the engine for a lock which outlasts the job, so a
slow step is not handed to a second worker while the first is still busy
with it. A job the engine hands out again — after a lapsed lock or a broken
stream — repeats its **result** instead of its work, when the server runs
with a `journal_dir`:

```sh
tret server -config config.yaml -journal-dir ./data/journal
```

The journal is keyed on the engine job key. It is the closest the platform
comes to exactly once: a step whose outcome is unknown — a worker which died
between the side effect and the record of it — may still run again.

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

## The operator commands

The engine keeps the state of every run, and `tret` reads it:

```sh
tret status                      # the running instances (all with -state all)
tret instance <key>              # one instance and its variables
tret jobs <key>                  # the jobs of an instance
tret incidents <key>             # the incidents of an instance
tret resolve <incident-key>      # let the failed step run again
tret complete <job-key>          # answer a human task from the terminal
tret capabilities                # the job types and commands of this build
```

The commands take the engine at `-engine` (default `http://localhost:8080`).
`tret resolve` gives the failed job one retry by default; `-retries` names
another number. A job of a human task is answered with the variables the
task asks for:

```sh
tret complete <job-key> -variables answer.json
```

## Status

Early development. Nothing is deployed or executed automatically; side effects
happen only when a deployed process schedules a job. The worker contracts, the
limits and the delivery semantics are documented in
[docs/workers.md](docs/workers.md). The journal which makes a redelivered job
repeat its result exists, but its store keeps its records on one node; a
store several nodes can reach is open. Output spill — routing large exec
output and long model content out of the process variables — is open, as are
per-process variable budgets and resumable chat (see [TODO.md](TODO.md)). The old `agent_loop` process and `agent-*` workers were removed (see git
history). The new minimal terminal chat uses an embedded plain BPMN loop plus
the generic workers; resumable transcripts and richer tool support remain open.

The code review of the current state, with the findings and their evidence,
is in [docs/mistakesToFix.md](docs/mistakesToFix.md); the plan for the
platform is in [docs/futurePlans.md](docs/futurePlans.md).

## License

[GPL-3.0](LICENSE)
