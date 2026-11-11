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
	go run ./cmd/server $(if $(CONFIG),-config $(CONFIG),)

run-agent:
	go run ./cmd/agent -goal "$(GOAL)" -engine "$(ENGINE)" $(ARGS)

deploy:
	go run ./cmd/server

e2e: up
	@BIN=$$(mktemp -d); \
	trap 'kill $${SERVER_PID:-0} 2>/dev/null; rm -rf $$BIN' EXIT; \
	go build -o $$BIN/server ./cmd/server && go build -o $$BIN/agent ./cmd/agent || exit 1; \
	MOCK_LLM=1 $$BIN/server & SERVER_PID=$$!; \
	sleep 2; \
	printf '/exit\n' | $$BIN/agent -goal "echo hi"
