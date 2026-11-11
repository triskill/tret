SERVER ?= http://localhost:8080
CONFIG ?=
GOAL ?=
.PHONY: up down build test run-agent run-server deploy e2e

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
	go run ./cmd/agent -goal "$(GOAL)" -server "$(SERVER)" $(if $(CONFIG),-config $(CONFIG),)

deploy:
	go run ./cmd/server

e2e: up build
	@MOCK_LLM=1 go run ./cmd/server & SERVER_PID=$$!; \
	sleep 4; \
	printf 'hello\nsubmit\n' | go run ./cmd/agent -goal "echo hi"; \
	status=$$?; \
	kill $$SERVER_PID 2>/dev/null; \
	exit $$status
