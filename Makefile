BINARY := echo
GO ?= go
DOCKER ?= docker

.PHONY: build test lint vet migrate docker clean

build:
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/echo

test:
	$(GO) test ./...

lint: vet

vet:
	$(GO) vet ./...

migrate:
	@echo "migrations are introduced in Phase 1"

docker:
	BUILDX_GIT_INFO=false $(DOCKER) build -f docker/Dockerfile -t echo:dev .

clean:
	rm -f $(BINARY)
