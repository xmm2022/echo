BINARY := echo
GO ?= go
DOCKER ?= docker
MIGRATE_DSN ?= file:./echo-migrate-check.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)

.PHONY: build test lint vet migrate docker clean

build:
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/echo

test:
	$(GO) test ./...

lint: vet

vet:
	$(GO) vet ./...

migrate:
	rm -f echo-migrate-check.db echo-migrate-check.db-wal echo-migrate-check.db-shm
	$(GO) run ./cmd/echo migrate --dsn '$(MIGRATE_DSN)' --direction up
	$(GO) run ./cmd/echo migrate --dsn '$(MIGRATE_DSN)' --direction down
	rm -f echo-migrate-check.db echo-migrate-check.db-wal echo-migrate-check.db-shm

docker:
	BUILDX_GIT_INFO=false $(DOCKER) build -f docker/Dockerfile -t echo:dev .

clean:
	rm -f $(BINARY)
