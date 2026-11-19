# One-call request classifier

This process references only generic platform capabilities. It makes one
`llm-call-v1` completion, validates the response against a JSON Schema, and lets the BPMN
gateway route on `classification.structured_output.approved`. Approved requests
run the application-owned `example-report-v1` worker. Denied requests end without
calling it. Invalid model output creates an incident; it is not a denial.

The report worker returns deterministic process variables and has no external
side effects. This is a wiring example, not a safety filter or authorization
boundary. Use trusted BPMN definitions and inputs.

## Run

From the repository root, with Go and Docker Compose available:

```sh
make up
cp config.example.yaml config.yaml
# Configure the model endpoint in config.yaml.
go run ./cmd/tret server -config config.yaml -workers llm-call-v1
```

In another terminal, register only the application's custom worker:

```sh
go run ./examples/classifier/worker -engine-grpc localhost:9090
```

Deploy the process and start it with the example input:

```sh
go run ./cmd/tret deploy -file examples/classifier/classifier.bpmn
go run ./cmd/tret start -process request_classifier -variables examples/classifier/variables.json
```

Both commands print an engine key. Inspect the instance in ZenBPM UI at
<http://localhost:8081>. Approved runs contain `classification`, `queue`, and
`report` variables. Denied runs contain `classification` but no `report`.
Edit the user message in `variables.json` to try another request. Provider
support for OpenAI-compatible `response_format: json_schema` is required.
The server-owned `default` profile uses the configured model endpoint; process
variables cannot supply an endpoint or API key.

## Mappings and extension point

`Classify` maps the process variable `request` into task-local `llm_request`.
The generic worker returns `llm_result`, which BPMN maps into `classification`.
`Report` explicitly maps its inputs and publishes `report` and `queue`.
No worker advances tokens or starts the next task.

Copy `report.Handle` or import
`github.com/triskill/tret/examples/classifier/report` to register the same public
`zenclient.WorkerFunc` in another executable. Custom workers do not need tret's
internal packages or CLI changes.

## Tests

```sh
go test ./examples/classifier/...
# Dedicated engine, no other llm-call-v1 or example-report-v1 workers:
TRET_INTEGRATION=1 go test -v -run TestToolkitClassifierLive ./internal/integration
```

The live test uses a local mock HTTP provider. It verifies approved and denied
routing, custom-worker call counts, and an incident for schema-invalid output.
It does not require a real model. `TRET_INTEGRATION_REST` and
`TRET_INTEGRATION_GRPC` override engine addresses.
