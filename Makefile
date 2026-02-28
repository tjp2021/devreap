BINARY := devreap
MODULE := github.com/tjp2021/devreap
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/cli.Version=$(VERSION) \
	-X $(MODULE)/internal/cli.Commit=$(COMMIT) \
	-X $(MODULE)/internal/cli.BuildDate=$(DATE)

.PHONY: build test lint cover install clean

build:
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/devreap

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "Run 'go tool cover -html=coverage.out' for detailed report"

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/devreap

clean:
	rm -f $(BINARY) coverage.out
	rm -rf dist/

vet:
	go vet ./...

fmt:
	gofmt -w .
	goimports -w .

all: fmt vet lint test build
