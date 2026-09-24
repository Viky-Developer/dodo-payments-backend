# Environment defaults
DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/dodo_payments?sslmode=disable
MIGRATIONS_DIR ?= internal/db/migrations
BIN_DIR ?= bin
PORT ?= 8080

.PHONY: help build run test test-race tidy fmt vet clean migrate-create migrate-up migrate-down migrate-status migrate-reset migrate-version docker-up docker-down

help: ## Display list of available targets
	@echo "Available Makefile commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build the API binary
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/api cmd/api/main.go

run: ## Run the API server locally
	PORT=$(PORT) DATABASE_URL="$(DATABASE_URL)" go run cmd/api/main.go

test: ## Run all tests
	go test -v ./...

test-race: ## Run all tests with race detector
	go test -v -race ./...

tidy: ## Tidy Go module dependencies
	go mod tidy

fmt: ## Format all Go files
	go fmt ./...

vet: ## Run Go static analysis
	go vet ./...

clean: ## Remove compiled binaries
	rm -rf $(BIN_DIR)

## Database Migrations (Goose)
migrate-create: ## Create a new SQL migration (usage: make migrate-create name=my_migration)
	@if [ -z "$(name)" ]; then \
		echo "Error: name is required. Usage: make migrate-create name=my_migration"; \
		exit 1; \
	fi
	goose -dir $(MIGRATIONS_DIR) create $(name) sql

migrate-up: ## Apply all pending database migrations
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" up

migrate-down: ## Roll back the most recent database migration
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" down

migrate-status: ## Print status of all migrations
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" status

migrate-version: ## Print current database migration version
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" version

migrate-reset: ## Roll back all database migrations
	goose -dir $(MIGRATIONS_DIR) postgres "$(DATABASE_URL)" reset

## Docker Compose
docker-up: ## Start PostgreSQL and supporting services in background
	docker compose up -d

docker-down: ## Stop all docker compose services
	docker compose down
