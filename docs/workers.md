# Worker contracts

## Ownership

ZenBPM owns scheduling, gateways, waiting, and process variables. A worker
performs one task and returns its result. Workers must not implement hidden
agent loops. BPMN input/output mappings bind application variables to task
contracts. Deploying BPMN does not install workers: each referenced job type
must have a running subscriber. New workers register directly through ZenBPM's
job API; no tret edit or plugin loader is needed. See `examples/classifier`.

This POC assumes trusted process authors, private engine access, and trusted
local users. Tools request actions and workers execute them — a process that
schedules `exec-command-v1` runs commands with the server's permissions. Keep
model credentials in server config; do not put secrets in process variables or
task headers.

## `llm-call-v1`

One non-streaming OpenAI-compatible completion, no conversation storage, no
tool execution, no internal retry. Independent calls in separate BPMN branches
are safe because there is no implicit shared history.

Input variable `llm_request`:

```json
{
  "model_profile": "default",
  "messages": [{"role": "user", "content": "Classify this request"}],
  "output_schema": {
    "type": "object",
    "properties": {"approved": {"type": "boolean"}},
    "required": ["approved"],
    "additionalProperties": false
  },
  "max_tokens": 128,
  "temperature": 0
}
```

Required: named `model_profile` and nonempty `messages`. Optional: native OpenAI
`tools`, JSON `output_schema`, positive `max_tokens`, `temperature` in [0,2].
No tools are offered unless supplied. The POC supports string content and
text/image_url content parts (no media fetching by tret). Native message fields
such as `tool_calls` and `tool_call_id` are retained. Unknown request fields are
rejected. The profile name resolves a server-owned endpoint, model, and API
key; inputs can't override them.

Output variable `llm_result` (`content`, `structured_output`, `tool_calls`,
`finish_reason`, `usage`, `model`, `request_id`). `structured_output` appears
only for schema requests after local JSON Schema validation; external schema
loading is disabled. The provider must support the OpenAI `response_format:
json_schema` request; there is no silent fallback. A schema response that is a
tool call without matching JSON content fails validation. Other calls return
tool calls for BPMN to route to separate workers. Usage/model/IDs depend on
provider metadata; missing fields are not fabricated. Responses bigger than
8 MiB are rejected; the HTTP call timeout is 120 s. These are transport limits,
not per-process budget — BPMN owns budgets with its own counters and gateways.

Failures come in two kinds.

A failure with an error code is a BPMN error: ZenBPM catches it with a
matching error handler, and without one it creates an incident. The engine
does not spend a retry for it, so the `retries` attribute of the task
definition has no effect.

A failure *without* an error code spends one of the job's retries: the engine
hands the job out again after the `retryBackoff` of the task definition, and
the last attempt creates an incident.

| failure | code | retries |
|---|---|---|
| the request is not usable | `invalid_input` | none — BPMN error |
| the provider refused the request or answered something unusable | `provider_error` | none — BPMN error |
| the provider refused the credentials | `provider_auth` | none — BPMN error |
| the answer does not match `output_schema` | `invalid_output` | none — BPMN error |
| the provider is busy, failed on its own side, or was not reached | — | one per attempt |

The last row is the one a process wants to survive: set `retries` and
`retryBackoff` on the task definition and a busy provider is waited out
instead of ending the loop. The provider's answer body is never put into the
error; only the status is.

## `exec-command-v1`

One `bash --noprofile --norc -c` command per job. Runs with the server's permissions in the
server-configured `exec_root`; there is no sandbox and no environment beyond HOME/TMPDIR/LANG.
Treat it as a tool the process deliberately invokes, not a security boundary. The shell reads
no profile file, so the environment of a command does not depend on the files in the
workspace.

Input variable `exec_request`:

```json
{"command": "pytest -q", "timeout_sec": 60}
```

`command` is required (non-empty, no NUL, ≤ 16 KiB), `timeout_sec` optional in
(0, 120], default 60. Unknown fields are rejected.

Output variable `exec_result`:

```json
{"exit_code": 0, "stdout": "...", "stderr": "", "timed_out": false,
 "cancelled": false, "truncated": false, "duration_ms": 412}
```

Exit codes, timeouts, and capped output (32 KiB per stream, `truncated` flag)
are ordinary results for BPMN to route on; only request-validation problems are
`invalid_input` worker errors. A command which fails is not a worker error at
all: it is a result the process routes on. The worker has no retryable
failure of its own, so the `retries` attribute of the task definition only
matters for the redelivery of the job. Commands run in their own process group
with TERM→KILL escalation. One attempt plus possible redelivery means
commands may run more than once; idempotent commands should carry their own
dedup key.

## Authoring another worker

1. Choose a versioned job type and document required inputs and outputs.
2. Validate inputs before side effects. Publish outputs only via mappings.
3. Use normal result values for valid business outcomes, including rejection.
4. Define technical error codes and whether BPMN catches them or the process
   stops at an incident.
5. Declare side effects, replay behavior, time/resource limits, cancellation,
   and which external credentials/permissions your worker needs.
6. Register the type in a separate process using `worker.Subscribe`, which
   asks the engine for a lock which outlasts the job, or
   `zenclient.RegisterWorkerWithOptions` with an explicit
   `zenclient.WithLockDuration`. The public `pkg/worker.Registry` provides
   optional explicit dispatch for a multi-type worker process. Configure it
   before serving; unknown types fail.

### Locks, redelivery and the job key

The engine reserves a delivered job for a worker until the lock lapses, then
hands it out again. A worker whose lock is shorter than its job therefore sees
its job several times, running at the same time. Subscribe with a lock longer
than the longest run of the handler — `pkg/worker` refuses a subscription
whose lock does not outlast the declared runtime — and renew it with
`ExtendDeliveryLock` for work that may run long.

A job which the engine hands out again after a broken stream is *the same
step*. Key every side effect on the engine job key (`WaitingJob.Key`), not on
the attempt and not on the delivery token: those two change per delivery, so
an effect keyed on them runs again on every retry. Persist the result of the
step next to the key and return the stored result when the job comes back;
that is what turns a redelivery into a repeat instead of a second execution.

### The journal

`pkg/journal` is that store. The built-in workers use it when the server is
started with a `journal_dir`:

* the step is keyed on the engine job key;
* a claim on the step keeps two workers from running it at the same time — the
  worker which holds the claim is the only one which runs it, the others fail
  the job so the engine hands it out again;
* the result of the step is stored next to the key, and a redelivery returns
  the stored result instead of running the step again;
* a step which *failed* is not stored: the next delivery runs it again.

The `file` journal keeps its records in a directory, so they survive the
restart of a worker and are shared by the workers of one node. A directory on
a filesystem of several nodes needs a store all of them reach, which decides
the winner of a claim in one place.

What the journal cannot close: a worker which dies between the side effect
and the stored result leaves a step whose outcome is unknown. The claim
expires and the step runs again. The window is narrow, and it is the price of
not having the effect and its record in one transaction.

Without a journal the workers behave as they did before: the work of a
redelivered job runs again.

### Writing a worker

A worker returns the concrete `*zenclient.WorkerError`, never an `error`
interface: a nil `*WorkerError` in an `error` variable is not nil, and the
job would be failed although it succeeded.

### Delivery and recovery

One attempt does not mean one execution. Engine job redelivery is still
possible, including handing a second attempt of a job whose first attempt ran;
an LLM call can be billed twice and a command can run twice. A broken job
stream does not cancel the work in flight: the built-in workers run the call
or the command under their own deadline, so a reconnect costs a redelivery but
not a half-finished side effect.

Idempotent workers should use an operation identity tied to the engine job
and persist known results. Non-idempotent workers should stop for manual
reconciliation on unknown outcomes. The journal of the built-in workers
repeats the result of a redelivered job; it does not fence the effect, and
it provides no automatic safe replay of a step whose outcome is unknown.
Resolving an incident is an operator action, not a guarantee that rerunning a
side effect is safe.
