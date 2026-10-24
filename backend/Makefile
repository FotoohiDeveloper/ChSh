.PHONY: help build test test-int race vet loadtest smoke secrets up down logs backup migrate dev fmt

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/'

build: ## compile the binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/chador ./cmd/chador

fmt: ## format
	gofmt -w .

vet: ## static checks
	go vet ./...

test: ## unit tests only (no database needed)
	go test ./internal/domain ./internal/platform

# Integration tests need a throw-away Postgres + Redis. They WIPE the database named in TEST_DATABASE_URL.
#   TEST_DATABASE_URL=postgres://chador:chador@localhost:5432/chador_test?sslmode=disable
#   TEST_REDIS_URL=redis://localhost:6379/9   TEST_ALLOW_RESET=1
test-int: ## full suite incl. integration tests
	go test ./... -count=1

race: ## full suite with the race detector
	go test ./... -count=1 -race

loadtest: ## HTTP load test (needs the same variables as test-int)
	LOADTEST=1 go test ./internal/api -run TestLoad -v -count=1

smoke: ## run the real binary the way production does (needs psql, redis-cli, jq)
	./scripts/smoke.sh

secrets: ## generate deploy/secrets/* (never overwrites)
	./deploy/init-secrets.sh

up: ## start the production stack
	cd deploy && docker compose up -d --build

down: ## stop it
	cd deploy && docker compose down

logs: ## follow API and worker logs
	cd deploy && docker compose logs -f api worker

backup: ## database backup into deploy/backups
	./deploy/backup.sh

migrate: ## apply migrations (inside the running stack)
	cd deploy && docker compose run --rm migrate

dev: ## dependencies in Docker, API on the host with mock vendors
	cd deploy && docker compose -f docker-compose.dev.yml up -d
	APP_ENV=dev DATABASE_URL=postgres://chador:chador@localhost:5432/chador?sslmode=disable \
	PII_ENC_KEY=$$(openssl rand -base64 32) PII_IDX_KEY=$$(openssl rand -base64 32) \
	go run ./cmd/chador serve
