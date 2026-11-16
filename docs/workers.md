# Worker contracts

## Ownership

ZenBPM owns scheduling, gateways, waiting, and process variables. A worker performs
one task and returns its result. Workers must not implement hidden agent loops.
BPMN input/output mappings bind application variables to task-local contracts.
Deploying BPMN does not install workers: each referenced job type must have a
running subscriber. New workers can register directly through ZenBPM's job API;
no tret server edit or plugin loader is needed. See `examples/classifier`.

This POC assumes trusted process authors, private engine access, and trusted local
users. Tool definitions request actions; they are not execution authorization.
The generic LLM worker never runs tools. Keep model credentials in server config.
Do not put secrets in workflow variables or task headers.

## `llm-call-v1`

One non-streaming OpenAI-compatible completion, no conversation storage, no route
classification, no tool execution, no internal retry. It is safe to use independent
calls in separate BPMN branches because there is no implicit shared history.

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
`tools`, JSON `output_schema`, positive `max_tokens`, `temperature` in [0,2]. No
tools are offered unless provided. The POC supports string content and text/image_url content parts (no media fetching
by tret). Native message fields such as `tool_calls` and
`tool_call_id` are retained. Unknown request fields are rejected. The profile name
resolves a server-owned endpoint, model, and API key; input cannot override them.

Output variable `llm_result`:

```json
{
  "content": "{\"approved\":true}",
  "structured_output": {"approved": true},
  "tool_calls": [],
  "finish_reason": "stop",
  "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
  "model": "provider-model-id",
  "request_id": "provider-request-id"
}
```

`structured_output` is present only for schema requests, after local JSON Schema
validation. External schema loading is disabled. The provider must support the
OpenAI `response_format: json_schema` request; there is no silent fallback. A
schema request whose response is a tool call without matching JSON content fails
validation. Other calls return tool requests for BPMN to route to separate workers.
Usage/model/IDs depend on provider metadata; missing fields are not fabricated.
Responses larger than 8 MiB are rejected; the HTTP call timeout is 120 seconds.
These are transport limits, not a context-selection policy or a per-run budget.
Map only bounded inputs into each task; generic BPMNs must define their own call
budgets rather than relying on the bundled agent's budget tasks.

Failures use `invalid_input`, `provider_error`, or `invalid_output` WorkerError
codes and zero remaining retries. ZenBPM can catch codes with BPMN error handlers;
without a handler it creates an incident. These errors never become an agent
`route`. HTTP non-success is a provider error; schema mismatch is invalid output.

## Authoring another worker

1. Choose a versioned job type and document required inputs and outputs.
2. Validate inputs before side effects. Publish outputs only through mappings.
3. Use normal result values for valid business outcomes, including rejection.
4. Define technical error codes and whether BPMN catches them or stops at an incident.
5. Declare side effects, replay behavior, time/resource limits, cancellation, and
   which external credentials/permissions the worker needs.
6. Register its type in a separate process using `zenclient.RegisterWorkerWithOptions`.
   The public `pkg/worker.Registry` optionally provides explicit dispatch for a
   multi-type worker process. Configure it before serving; unknown types fail.

### Delivery and recovery

One attempt does not guarantee one execution. Engine job redelivery remains
possible, including during an existing attempt. LLM calls can be billed twice;
shell commands can run twice. Idempotent workers should use an operation identity
bound to the engine job and persist known results. Non-idempotent workers must
stop for manual reconciliation when an outcome is unknown. This POC does not
provide a journal, fencing, or automatic safe replay. Resolving an incident is an
operator action, not a promise that rerunning a side effect is safe.

## Bundled agent (application-specific)

`agent-decide`, `agent-execute`, `agent-budget`, `agent-fail-report`, and human
`agent-reply` belong to `bpmn/agent_loop.bpmn`. Their unversioned variables and
`tool/message/finish/fail` vocabulary are legacy example contracts, not toolkit
standards. `agent-decide` still uses textual conversation/tool summaries; native
structured tool history is supported by `llm-call-v1`, not retrofitted here.
The agent's shell runs with server permissions in one server workspace, not a
sandbox or per-run filesystem. Selecting only `llm-call-v1` avoids registering the
shell worker. Human tasks can be completed by other clients; `tret agent` only
implements the bundled `agent-reply` interaction.

Legacy agent transcripts are capped at 1 MiB and 100 entries. Reaching a bound or
reading corrupt JSONL fails the run rather than silently trimming history. New
transcripts use instance identity and SHA-256 filenames; business-key histories
from older versions are not migrated. New directories/files use 0700/0600;
existing permissions are not changed. A Store serializes its own appends, but
multiple server processes sharing a state directory are unsupported. The engine
REST client now uses a 15-second HTTP timeout. Failure reporting remains best
effort if the transcript itself is unavailable, preserving the original failure.
