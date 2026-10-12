# Build and run control, usage: make [prod|lint|test|run|debug]
MODULE       := github.com/Valentin-Kaiser/backup
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_FILE ?= ../../../bin/control
DATA_PATH    ?= ../../../data

LDFLAGS := -s -w \
	-X github.com/valentin-kaiser/go-core/version.GitTag=$(VERSION) \
	-X github.com/valentin-kaiser/go-core/version.GitCommit=$(COMMIT) \
	-X github.com/valentin-kaiser/go-core/version.GitShort=$(COMMIT) \
	-X github.com/valentin-kaiser/go-core/version.BuildDate=$(BUILD_DATE)

.PHONY: prod lint test run debug

prod:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(RELEASE_FILE) .

lint:
	golangci-lint run ./../...

test:
	go test ./../../../...

run:
	go run . --path $(DATA_PATH)

debug:
	go run . --debug --path $(DATA_PATH)
