## Prerequisites

- **Go**: see `go.mod` for the version.
- **make** and **git**.
- **Docker Engine + Compose v2**: only for the integration tests.

## Setup

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
go install github.com/evilmartians/lefthook@latest
lefthook install
```

`lefthook install` wires up two hooks: **pre-commit** (lint, test, build) and
**commit-msg** (Conventional Commits, see below).

## Build and test

```bash
make build             # bin/deeplo (daemon + CLI)
make check             # fmt, vet, and unit tests
make test-unit         # unit tests, no Docker
make test-integration  # integration tests, needs local Docker
make lint              # golangci-lint
```

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org):
`type(scope): summary`, with an imperative, lowercase summary. The scope is optional
(`engine`, `planner`, `config`, ...); a `!` marks a breaking change (`feat!: drop config v1`).

Types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`.

## Pull requests

- Branch off `main` and keep each PR to a single change.
- Make sure `make check lint` passes. CI runs the same on every PR.
