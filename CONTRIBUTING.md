# Contributing to zkteco-biota-client

Thank you for considering a contribution! This document explains how to get
started.

## Prerequisites

- Go 1.26 or higher
- `make`

All lint and analysis tools (golangci-lint, govulncheck, deadcode) are pinned
in `internal/tools/go.mod` and built into `./bin` by the Makefile, so nothing
else needs to be installed. That module is separate from the library's
`go.mod`, which stays dependency-free.

## Getting started

```bash
git clone https://github.com/s0x90/zkteco-biota-client.git
cd zkteco-biota-client
go test -race ./...
```

## Development workflow

1. Fork the repository and create a feature branch from `master`.
2. Make your changes.
3. Run the full check suite before submitting:

   ```bash
   make build
   make test
   make test-tz
   make lint
   ```

4. Open a pull request against `master`.

Formatting (gofumpt and goimports) is enforced by golangci-lint; `make fmt`
applies it in place.

`make lint` runs exactly the checks CI runs and reports every failing check,
not just the first. It needs network access: govulncheck downloads the
vulnerability database on every run, and a fetch error there is a network
problem, not a finding in your code. Each check is also available on its own
(`make lint-deadcode`, `make lint-golangci-lint`, ...).

govulncheck also reports vulnerabilities in the Go standard library itself,
keyed on the toolchain that built the binary. CI installs the latest patch
release of the Go version in `go.mod`; if your local toolchain is older, update
it before reading a standard-library finding as a problem with the code.

`make build` compiles the library and the example program and fails when the
module gains a dependency: this client is dependency-free by design.

`make test-tz` re-runs the suite in a zone west of UTC. `NewDate` and
`DateTime` interpret the server's naive timestamps in the configured zone, and
a test that accidentally relies on the host zone would pass on a runner at or
east of UTC and fail elsewhere.

Adding or removing a tool means touching three places: the `tool` directives in
`internal/tools/go.mod`, the matching `lint-*` target in the `Makefile`, and the
CI matrix in `.github/workflows/lint.yml`. `make check-ci` fails when they
disagree and runs as part of `make lint` and in CI.

`make clean` removes the built tool binaries in `./bin`.

To bump or add a tool, work inside the tools module and never point `go get`
or `go mod tidy` at it from the repo root with `-modfile`: that makes the go
command treat the whole repo as the tools module and pull the published
library from the proxy.

```bash
cd internal/tools
go get -tool golang.org/x/vuln/cmd/govulncheck@vX.Y.Z   # or `go get -tool <pkg>@<version>` for a new tool
cd ../.. && make tools-tidy
```

Without `make` (for example on Windows), build the same binaries by hand and
run the commands from the matching `lint-*` targets in the `Makefile`:

```bash
cd internal/tools
go build -o ../../bin/ $(go list tool)
cd ../..
bin/deadcode -test ./...
```

If a tool bump raises the `go` directive in `internal/tools/go.mod` past the
Go version CI installs, the tool build fails on purpose. Either keep the older
tool version or raise the project's Go version deliberately in a separate
change.

## Code style

- Follow standard Go conventions (`gofmt`, `goimports`).
- The project uses `golangci-lint` v2 with the config in `.golangci.yml`.
  Run it locally (see above) to catch issues before pushing.
- Keep the library at **zero external dependencies** (pure stdlib).
- Use US English spelling in comments and strings (enforced by `misspell`).
- Exported identifiers carry a doc comment. Behavior that differs between
  server generations is documented on the field or method it affects, with
  the generation named.

## Tests

All changes should include tests. This is partly enforced by the `deadcode`
check: the library has no `main` of its own, so its only reachability roots
are `examples/` and the test files. An exported function or method that no
test or example calls is reported as dead and fails CI. If you add exported
functions, add a test or an example that exercises them in the same change.

`deadcode` reports functions and methods only. Exported types, constants and
variables are not covered by any check, so tests for those are on you and your
reviewer.

Tests run against an in-process fake server (`newFakeServer` in
`client_test.go`) that speaks both the 8.x and the 9.0 dialect. A behavior
observed on a real server belongs there as a fixture, with the generation and
the observation in a comment. Fixtures must not carry data from a real
installation: no real serial numbers, PINs, addresses or people.

Run the full suite with race detection:

```bash
go test -race -cover ./...
```

You can generate an HTML coverage report:

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Trying changes against a real server

`examples/basic` talks to a live server and is the quickest way to check a
change end to end:

```bash
BIOTIME_URL=http://biotime.example.com:8080 BIOTIME_USER=admin BIOTIME_PASS=secret \
  go run ./examples/basic -version 8 -jwt -since 24h -debug
```

Never commit the credentials or the address of a server you used; `.env` files
are ignored by git for that reason.

## Commit messages

Write clear commit messages with a summary line (imperative mood, ~72 chars)
and an optional body explaining the "why" behind the change.

## Reporting issues

Open an issue on GitHub with steps to reproduce, expected behavior, and actual
behavior. Include your Go version and the server version (8.x or 9.0, and the
build if you know it): the two generations differ in enough details that a
report without it is hard to act on. When the server answered something
unexpected, the raw body from `Error.Body` or a `WithLogger` debug trace
helps, with credentials and personal data removed.

## License

By contributing, you agree that your contributions will be licensed under the
MIT License.
