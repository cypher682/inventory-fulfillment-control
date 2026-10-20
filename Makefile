.PHONY: build test test-race fmt vet run tidy up down

build:
	go build -trimpath -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/inventory ./cmd/inventory

test:
	go test ./...

test-race:
	CGO_ENABLED=1 go test -race ./...

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
