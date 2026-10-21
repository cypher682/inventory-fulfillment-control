TEST_DATABASE_URL ?= postgres://inventory:inventory@localhost:5442/inventory?sslmode=disable
TEST_NATS_URL ?= nats://localhost:4222

.PHONY: build test test-race test-integration fmt vet run tidy up down

build:
	go build -trimpath -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/inventory ./cmd/inventory

test:
	go test ./...

test-race:
	CGO_ENABLED=1 go test -race ./...

test-integration: up
	@until docker compose -f deploy/docker-compose.yml exec -T postgres pg_isready -U inventory -d inventory >/dev/null 2>&1; do sleep 0.5; done
	@sleep 1
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) TEST_NATS_URL=$(TEST_NATS_URL) go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

run:
	go run ./cmd/inventory

tidy:
	go mod tidy

up:
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down -v
