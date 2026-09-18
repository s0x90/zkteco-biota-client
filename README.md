# zkteco-biota-client

[![Go Reference](https://pkg.go.dev/badge/github.com/s0x90/zkteco-biota-client/biotime.svg)](https://pkg.go.dev/github.com/s0x90/zkteco-biota-client/biotime)
[![Test](https://github.com/s0x90/zkteco-biota-client/actions/workflows/test.yml/badge.svg)](https://github.com/s0x90/zkteco-biota-client/actions/workflows/test.yml)
[![Lint](https://github.com/s0x90/zkteco-biota-client/actions/workflows/lint.yml/badge.svg)](https://github.com/s0x90/zkteco-biota-client/actions/workflows/lint.yml)
[![codecov](https://codecov.io/gh/s0x90/zkteco-biota-client/graph/badge.svg)](https://codecov.io/gh/s0x90/zkteco-biota-client)
[![Go Report Card](https://goreportcard.com/badge/github.com/s0x90/zkteco-biota-client)](https://goreportcard.com/report/github.com/s0x90/zkteco-biota-client)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A dependency-free Go client for the REST API of the ZKTeco **ZKBio Time**
attendance server, formerly **BioTime**. It works against both the legacy
BioTime 8.x servers and the current ZKBio Time 9.0+ servers, and hides the
differences between them: pagination parameters, response envelopes, login
endpoints, expanded-or-bare related objects and the server's zone-less
timestamps are all handled inside the client.

If you talk to the devices directly rather than to the server, see
[zkteco-adms](https://github.com/s0x90/zkteco-adms), the companion library
for the ADMS push protocol.

## Contents

- [Features](#features)
- [Server compatibility](#server-compatibility)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Authentication](#authentication)
- [Resources](#resources)
- [Listing, filtering and pagination](#listing-filtering-and-pagination)
- [Creating and updating](#creating-and-updating)
- [Endpoints the package does not model](#endpoints-the-package-does-not-model)
- [Errors](#errors)
- [Time zones](#time-zones)
- [Exporting punches](#exporting-punches)
- [Credentials in records and logs](#credentials-in-records-and-logs)
- [Client options](#client-options)
- [Example program](#example-program)
- [Notes on 8.x servers](#notes-on-8x-servers)
- [Notes on the 9.0 manual](#notes-on-the-90-manual)
- [Development](#development)
- [License](#license)

## Features

- **Both server generations** from one API: pick `Version8` or `Version9`,
  everything else is shared.
- **Zero dependencies.** Pure standard library, Go 1.26+.
- **Typed services** for employees, departments, areas, positions, terminals
  and transactions, plus an escape hatch (`Do`, `Get`, `Post`) for every
  other endpoint.
- **Lazy login and transparent re-login** on `401`, so long-running programs
  survive JWT expiry.
- **Iterator-based pagination** (`iter.Seq2`) that follows the server's
  `next` links on demand, stops when you `break`, and refuses to loop on a
  server that repeats a page.
- **Lenient decoding** of the server's inconsistent JSON: numbers as strings,
  strings as numbers, related objects expanded or as bare ids, several
  timestamp layouts.
- **Partial updates**: `Update` sends `PATCH` with only the fields you set.
- **Custom employee attributes** defined in the server UI round-trip through
  `Extra`.
- **Credentials never leak**: tokens and passwords are not logged, and the
  device PIN the server returns in clear text is redacted in `fmt`, `slog`
  and JSON output.
- **Structured errors** with `errors.Is` sentinels, the server's message and
  per-field validation errors.
- **Safe for concurrent use.**

## Server compatibility

| | BioTime 8.x (legacy) | ZKBio Time 9.0+ |
|---|---|---|
| Select with | `biotime.WithVersion(biotime.Version8)` | `biotime.WithVersion(biotime.Version9)` (default) |
| API docs | `http://<server>/api/personnel_docs/`, `/api/iclock_docs/`, `/api/att_docs/` (login required; `/api/docs/` lists only the auth endpoints) | *ZKBio Time 9.0 API User Manual* |
| Auth | `/jwt-api-token-auth/` (`Authorization: JWT …`) or `/api-token-auth/` (`Authorization: Token …`) | `/api-token-auth/` (`Authorization: Token …`) |
| Page size parameter | `page_size` (`limit` is ignored) | `limit` |
| List envelope | `{count,next,previous,results}` per the docs; the tested build already returns the 9.0 shape | `{count,next,previous,code,msg,data}` |
| Attendance flags on employees | top-level `enable_att`, `enable_overtime`, `enable_holiday` | nested `attemployee` object |

The client decodes either list envelope regardless of the configured version,
reads the attendance flags from either shape through `Employee.AttendanceEnabled()`
and friends, and accepts all timestamp layouts seen across both generations.
The version setting only decides the page-size parameter name. Behaviour that
was verified on a live 8.x installation is listed under
[Notes on 8.x servers](#notes-on-8x-servers).

## Installation

```sh
go get github.com/s0x90/zkteco-biota-client/biotime
```

Requires Go 1.26 or newer. The module has no third-party dependencies.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/s0x90/zkteco-biota-client/biotime"
)

func main() {
	client, err := biotime.New("http://biotime.example.com:8080",
		biotime.WithCredentials("admin", "secret"),
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// Stream every punch of the last 24 hours, page by page.
	filter := &biotime.TransactionFilter{
		StartTime:   time.Now().Add(-24 * time.Hour),
		ListOptions: biotime.ListOptions{PageSize: 200, Ordering: "punch_time,id"},
	}
	for tx, err := range client.Transactions.All(ctx, filter) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(tx.PunchTime, tx.EmpCode, tx.PunchState, tx.TerminalSN)
	}
}
```

For a legacy 8.x server, name the generation and, if the server was set up
for it, the JWT login:

```go
client, err := biotime.New("http://biotime.example.com:8080",
	biotime.WithVersion(biotime.Version8),
	biotime.WithAuthScheme(biotime.AuthJWT),
	biotime.WithCredentials("admin", "secret"),
)
```

The base URL may carry a path prefix (`https://example.com/biotime`), which
is prepended to every request. Only `http` and `https` are accepted.

## Authentication

Login is lazy: the first request that needs a token obtains one with the
configured credentials. A request rejected with `401` triggers exactly one
re-login and retry, which keeps long-running programs working across JWT
expiry. Concurrent requests share one login; they never race to the auth
endpoint.

| Scheme | Login endpoint | Header | Notes |
|---|---|---|---|
| `AuthToken` (default) | `POST /api-token-auth/` | `Authorization: Token <t>` | Documented for 9.0, also served by 8.x. Tokens are static. |
| `AuthJWT` | `POST /jwt-api-token-auth/` | `Authorization: JWT <t>` | Documented for 8.x. Tokens expire; the client re-authenticates. |

- `biotime.WithToken(t)` uses a pre-issued token. Combined with
  `WithCredentials`, the token is used until the server rejects it, after
  which the client logs in again.
- `client.Login(ctx)` authenticates eagerly, for example to fail fast at
  start-up. `client.Token()` and `client.SetToken()` expose the token in use.
- Rejected credentials are reported by the server as a `400` validation
  response; the error nevertheless matches `ErrUnauthorized` (and
  `ErrValidation`, which is how the server frames it).
- Without a token and without credentials, the first request fails with
  `ErrNoCredentials`.

## Resources

| Service | Endpoint | Operations |
|---|---|---|
| `client.Employees` | `/personnel/api/employees/` | `List`, `All`, `Get`, `GetByCode`, `Create`, `Update`, `Delete` |
| `client.Departments` | `/personnel/api/departments/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Areas` | `/personnel/api/areas/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Positions` | `/personnel/api/positions/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Terminals` | `/iclock/api/terminals/` | `List`, `All`, `Get` |
| `client.Transactions` | `/iclock/api/transactions/` | `List`, `All`, `Get` |

Terminals and transactions are read-only through the services. 8.x accepts
`POST`, `PATCH` and `DELETE` on both endpoints; use `client.Do` for that.

Record types decode leniently. `FlexString`, `FlexInt` and `FlexFloat`
accept a number or a string holding one, because the server is inconsistent
about card numbers, device states and counters across versions and
endpoints. `Ref[T]` decodes a related object whether the server expanded it
(`{"id":1,"dept_name":…}`) or sent the bare id (`1`), and encodes as the id,
which is what write endpoints expect. Members the struct does not declare are
kept as raw JSON in `Extra` on employees, terminals and transactions.

## Listing, filtering and pagination

Every service has `List`, which returns one `Page[T]`, and `All`, which
returns an `iter.Seq2[T, error]`:

```go
page, err := client.Employees.List(ctx, &biotime.EmployeeFilter{
	Department:  3,
	ListOptions: biotime.ListOptions{PageSize: 50, Ordering: "emp_code"},
})
fmt.Println(page.Count, len(page.Results), page.HasNext())

for e, err := range client.Employees.All(ctx, nil) {
	if err != nil {
		return err
	}
	if e.EmpCode == "1042" {
		break // stops fetching; no further pages are requested
	}
}

depts, err := biotime.Collect(client.Departments.All(ctx, nil))
```

- **Filters** are typed per resource (`EmployeeFilter`, `TransactionFilter`,
  …) and embed `ListOptions` for `Page`, `PageSize`, `Ordering` and `Search`.
  A nil filter lists everything.
- **Typed filter fields match exactly**: `FirstName: "adm"` finds nobody.
  `ListOptions.Search` sends the server's free-text `search` parameter, and
  the `<field>_icontains` variants, or any parameter the struct does not
  model, go through `Params`:

  ```go
  &biotime.EmployeeFilter{Params: map[string]string{"emp_code_icontains": "10"}}
  ```

- **`All` follows the server's `next` links** but uses only their query
  string, never their host. Servers behind a proxy that advertise an
  internal address in `next` still work.
- **A server that repeats a page** ends the walk with an error instead of
  looping forever. The repeated page has already been yielded by then, so a
  consumer that writes as it reads should dedupe on `ID`.
- **The sequence can be ranged over more than once**, and concurrently; each
  walk starts from the first page.
- **`Employees.GetByCode`** scans every candidate page for the exact code,
  because some servers match `emp_code` as a prefix.

## Creating and updating

`Create` sends `POST`; `Update` sends `PATCH`, so only the fields you set are
changed. Params types use pointers for scalars: a nil pointer, a zero date
and a nil slice are omitted, and an empty, non-nil slice such as
`Area: []int{}` is sent and clears the assignment.

```go
emp, err := client.Employees.Create(ctx, &biotime.EmployeeParams{
	EmpCode:    new("1042"),
	FirstName:  new("Harry"),
	LastName:   new("Potter"),
	Department: new(1),
	Area:       []int{1},
	HireDate:   biotime.NewDate(time.Now()),
	Extra:      map[string]any{"Passport": "AB123"}, // custom fields defined in the server UI
})

_, err = client.Employees.Update(ctx, emp.ID, &biotime.EmployeeParams{CardNo: new("5659812")})

err = client.Employees.Delete(ctx, emp.ID)
```

On create the server requires `EmpCode`, `Department` and `Area`; 9.0 also
requires `FirstName`. Custom attributes that the administrator added in the
server UI come back in `Employee.Extra` as raw JSON and are written through
`EmployeeParams.Extra`.

### Attendance flags are verified after a write

Django REST framework silently drops request members it does not know. The
8.x attendance flags `EnableAtt`, `EnableOvertime` and `EnableHoliday` are
such members on a server that nests them differently, so after a `Create` or
`Update` that sets one of them the client reads the record back and compares:

- Every flag matches: the record is returned.
- A flag differs, or the record does not carry it at all: the error is an
  `*UnsupportedFieldError` matching `ErrUnsupportedField`. The write has
  nevertheless happened; on create the employee exists. The error carries the
  record, and the client never deletes on its own.
- The read-back itself failed: the error matches `ErrUnverified` instead,
  carries the record and the cause, and the fields are neither confirmed nor
  refuted. Read the record again rather than repeating the write.

## Endpoints the package does not model

Schedules, manual logs, device commands and the rest of the API are
reachable with `client.Do`, `client.Get` and `client.Post`, which handle
authentication, the trailing slash Django insists on, JSON encoding and
error mapping. `path` is relative to the base URL; `body` is encoded as JSON
unless it is an `io.Reader`, a `[]byte` or a `json.RawMessage`, which are
sent as is.

```go
var out map[string]any
err := client.Do(ctx, http.MethodPost, "/att/api/manualLogs/", nil, map[string]any{
	"employee": emp.ID, "punch_time": "2024-06-26 09:00:00", "punch_state": 0,
	"work_code": "", "apply_reason": "forgot card",
}, &out)
```

Route names differ between generations for some of them: manual logs live at
`/att/api/manualLogs/` on 9.0 and at `/att/api/manuallogs/` on 8.x.

## Errors

Every non-2xx response, and every 9.0 list response with a non-zero `code`,
is returned as a `*biotime.Error` carrying the status, the request, the
server's message, any per-field validation messages and the raw body.
Sentinels work with `errors.Is`:

```go
_, err := client.Employees.Get(ctx, 999)
switch {
case errors.Is(err, biotime.ErrNotFound):
case errors.Is(err, biotime.ErrUnauthorized):
case errors.Is(err, biotime.ErrValidation):
	apiErr, _ := errors.AsType[*biotime.Error](err)
	fmt.Println(apiErr.Fields) // map[emp_code:[This field is required.]]
}
```

| Sentinel | Matches |
|---|---|
| `ErrUnauthorized` | `401`, `403`, and a rejected login |
| `ErrNotFound` | `404` |
| `ErrValidation` | `400` with field errors |
| `ErrNoCredentials` | a request needed a token and none was configured |
| `ErrUnsupportedField` | a write was accepted but a field was ignored (see above) |
| `ErrUnverified` | a write was accepted but could not be read back |

Redirects are not followed and surface as a `3xx` error, because a followed
redirect would turn a `POST` into a `GET` and silently decode the wrong
resource. Response bodies are capped (32 MiB by default, see
`WithMaxBodySize`) and a larger body is an error rather than an allocation.

Error messages come back in the language requested with `WithLanguage`
(default `en`), so they are predictable whatever locale the server runs in.

## Time zones

ZKBio Time stores and returns wall-clock times without zone information. The
client interprets them, and encodes every `DateTime`, `Date` and
`StartTime`/`EndTime` filter it sends, in the zone returned by
`biotime.Location()`, which defaults to `time.Local`. That default is wrong
whenever the program runs in a different zone than the server, which is the
norm in containers (UTC), so call `biotime.SetLocation` once at start-up:

```go
loc, _ := time.LoadLocation("Europe/Moscow")
biotime.SetLocation(loc)
```

The setting is package wide: one process talks to servers in one zone.
Construct dates with `biotime.NewDate(t)`, which takes the calendar date of
`t` in that zone, the date the server would record for it.

## Exporting punches

Pagination is by page number over live data. Punches keep arriving and many
share a `punch_time`, so a walk ordered by `punch_time` alone is not stable
across pages. For a lossless export:

1. Order by `"punch_time,id"` so that rows with equal times have a stable
   order.
2. Bound the window with an `EndTime` in the past, so that rows inserted
   during the walk cannot shift the pages.
3. Continue from that `EndTime` on the next run.
4. Dedupe on `ID` if you write as you read; a server that repeats a page
   ends the walk with an error, but that page has already been yielded.

The filter is sent exactly as written; the client does not add an ordering
of its own.

## Credentials in records and logs

- `WithLogger` logs the method, URL, status, size and duration of every
  request at debug level. Tokens, passwords and request or response bodies
  are never logged.
- 8.x returns each employee's **device PIN in clear text** and the
  **self-service password hash**. The hash is dropped on decoding. The PIN
  is kept as `Employee.DevicePassword` of type `Secret`, which prints as
  `[redacted]` with the `fmt` verbs, with `slog` and with `encoding/json`;
  read it deliberately with `.Value()`.
- A JSON dump of an `Employee` is therefore a read model, not a migration
  format: decoding the placeholder back into a `Secret` is an error, so
  carry `DevicePassword.Value()` explicitly when copying employees between
  servers.

## Client options

| Option | Purpose |
|---|---|
| `WithVersion(v)` | server generation, `Version8` or `Version9` (default) |
| `WithCredentials(user, pass)` | username and password for lazy login and re-login |
| `WithToken(t)` | pre-issued token |
| `WithAuthScheme(s)` | `AuthToken` (default) or `AuthJWT` |
| `WithHTTPClient(*http.Client)` | custom transport, TLS settings, proxies; set `CheckRedirect` to return `http.ErrUseLastResponse` |
| `WithTimeout(d)` | per-request timeout of the default client (30s); no effect with `WithHTTPClient` |
| `WithMaxBodySize(n)` | cap on buffered response bodies and reader request bodies (32 MiB) |
| `WithLogger(*slog.Logger)` | debug-log every request (never logs tokens or passwords) |
| `WithUserAgent(s)` | custom `User-Agent` |
| `WithLanguage(tag)` | `Accept-Language` for server error messages (default `en`; `""` sends none) |
| `WithPageSizeParam(name)` | override the page size parameter for non-standard servers |

Options are independent and may be passed in any order; each validates its
input and `New` returns the first error.

## Example program

`examples/basic` lists devices, departments, employees and recent punches
from a live server:

```sh
BIOTIME_URL=http://biotime.example.com:8080 BIOTIME_USER=admin BIOTIME_PASS=secret \
  go run ./examples/basic -version 8 -jwt -since 24h
```

Add `-debug` to log every request.

## Notes on 8.x servers

Verified against a BioTime 8.x installation (Python 2.7, Django REST
framework):

- `limit` is ignored; only `page_size` changes the page size, and the server
  honours large values (5000 punches in one page). `WithVersion(Version8)`
  selects `page_size`.
- The tested build already wraps list responses in the 9.0 envelope
  (`code`/`msg`/`data`); `Page` decodes both shapes regardless of version.
- The `next` links carry the server's internal address; the client uses only
  their query string.
- Employees carry the attendance flags as top-level `enable_att`,
  `enable_overtime` and `enable_holiday`; read them with
  `Employee.AttendanceEnabled()` and friends, which also understand the 9.0
  nested form. Writing them is verified, see
  [Creating and updating](#creating-and-updating).
- `first_name` is not required on create; `emp_code`, `department` and `area`
  are.
- Transactions and terminals accept `POST`, `PATCH` and `DELETE`; the
  services stay read-only, use `client.Do` for writes.
- `start_time`/`end_time` accept a date or a date-time; an unparsable value
  yields an empty result rather than an error.
- Unknown query parameters and unknown `ordering` fields are silently
  ignored.
- `GET /att/api/manuallogs/` answers 500 on the tested build; only `POST`
  works.
- Error messages are localized to the server's configured language unless
  `Accept-Language` is sent; the client sends `en` by default.

## Notes on the 9.0 manual

- The manual documents area updates as `POST /personnel/api/areas/<id>/`; the
  server actually accepts `PATCH` like every other resource, and that is what
  `Areas.Update` uses.
- The manual's area list example shows a bare object; the server returns the
  usual paginated envelope.
- The 9.0 create response for an employee omits the attendance settings, so
  the client fetches the record to verify them (see above).

## Development

```sh
make build     # vet, compile, and assert the module stays dependency-free
make test      # go test -race -cover ./...
make test-tz   # the suite in a zone west of UTC
make lint      # golangci-lint, govulncheck, deadcode, CI matrix drift
make fmt       # gofumpt + goimports in place
```

The lint tools are pinned in `internal/tools/go.mod` and built into `./bin`
by the Makefile; nothing else needs to be installed. CI runs the same
targets on GitHub-hosted runners. `deadcode -test` treats every exported
function that no test or example reaches as an error, so new public entry
points ship with a test. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
details, including how to add or bump a tool.

The test suite runs against an in-process fake that speaks both dialects; no
server is needed. `examples/basic` is the manual end-to-end check against a
real one.

## License

[MIT](LICENSE)
