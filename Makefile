ENGINE ?= http://localhost:8080
CONFIG ?= config.example.yaml
BPMN ?=

.PHONY: up down build test run run-server deploy

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

deploy:
	@test -n "$(BPMN)" || { echo "set BPMN=path/to/process.bpmn"; exit 2; }
	go run ./cmd/tret deploy -file "$(BPMN)" -engine "$(ENGINE)"
