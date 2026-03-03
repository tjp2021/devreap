# Contributing to devreap

Thanks for your interest in contributing to devreap.

## Getting Started

```bash
git clone https://github.com/tjp2021/devreap.git
cd devreap
make build
make test
```

## Development

### Prerequisites

- Go 1.25+
- golangci-lint (`brew install golangci-lint`)

### Commands

```bash
make build       # Build binary
make test        # Run tests with race detector
make lint        # Run linters
make cover       # Run tests with coverage report
make install     # Install to $GOPATH/bin
make clean       # Remove build artifacts
```

### Code Style

- Run `gofmt` and `goimports` before committing (enforced by CI)
- All exported types and functions need GoDoc comments
- Follow standard Go conventions: https://go.dev/doc/effective_go

### Tests

- All new code needs tests
- Run `make test` before submitting — CI will reject failing tests
- Integration tests that spawn real processes use `//go:build !short` tags
- Use `go test -short ./...` to skip integration tests during development

### Adding a Pattern

1. Add a new entry to the appropriate YAML file in `internal/patterns/`
2. Each pattern needs: `name`, `args_regex`, `max_duration`, `signal`
3. Run `make test` to verify pattern loading
4. Test with `go run ./cmd/devreap scan -v` to see if your pattern matches

## Pull Requests

1. Fork the repo and create a branch from `main`
2. Add tests for new functionality
3. Run `make lint && make test` — both must pass
4. Keep PRs focused — one feature or fix per PR
5. Write a clear description of what changed and why

## Commit Messages

Use conventional format:

```
feat: add Docker container pattern
fix: prevent false positive on CursorUIViewService
test: add killer integration tests
docs: update README install instructions
```

## Reporting Bugs

Open an issue with:
- `devreap version` output
- `devreap doctor` output
- Steps to reproduce
- Expected vs actual behavior

## Security

See [SECURITY.md](SECURITY.md) for reporting vulnerabilities.
