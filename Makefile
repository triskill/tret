ENGINE ?= http://localhost:8080
CONFIG ?= config.example.yaml
GOAL ?=
ARGS ?=
.PHONY: up down build test run run-agent run-server deploy e2e

up:
	docker compose up -d

down:
	docker compose down

build:
	go build ./...

test:
	go test ./...

run: up
	@echo "Starting engine + server..."
	@$(MAKE) run-server CONFIG=$(CONFIG)

run-server:
	go run ./cmd/tret server $(if $(CONFIG),-config $(CONFIG),)

run-agent:
	go run ./cmd/tret agent -goal "$(GOAL)" -engine "$(ENGINE)" $(ARGS)

deploy:
	go run ./cmd/tret server

e2e: up
	@BIN=$$(mktemp -d); \
	trap 'kill $${SERVER_PID:-0} 2>/dev/null; rm -rf $$BIN' EXIT; \
	go build -o $$BIN/tret ./cmd/tret || exit 1; \
	MOCK_LLM=1 $$BIN/tret server & SERVER_PID=$$!; \
	for i in $$(seq 1 60); do \
		curl -sf "$(ENGINE)/v1/process-definitions?bpmnProcessId=agent_loop&onlyLatest=true" | grep -q agent_loop && break; \
		sleep 1; \
	done; \
	printf '/exit\n' | $$BIN/tret agent -goal "echo hi"
