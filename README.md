# zkteco-biota-client

A dependency-free Go client for the ZKTeco **ZKBio Time** (formerly **BioTime**)
attendance server REST API.

It supports both server generations:

| | BioTime 8.x (legacy) | ZKBio Time 9.0+ |
|---|---|---|
| Docs | `http://<server>/api/docs/` | *ZKBio Time 9.0 API User Manual* |
| Select with | `biotime.WithVersion(biotime.Version8)` | `biotime.WithVersion(biotime.Version9)` (default) |
| Page size parameter | `page_size` | `limit` |
| List envelope | `{count,next,previous,results}` | `{count,next,previous,code,msg,data}` |
| Auth | `/jwt-api-token-auth/` (`Authorization: JWT …`) or `/api-token-auth/` (`Authorization: Token …`) | `/api-token-auth/` (`Authorization: Token …`) |

The differences are handled inside the client: list pages decode either
envelope, related objects decode whether the server expands them or returns a
bare id, and timestamps decode the server's naive `2006-01-02 15:04:05` format.

Requires Go 1.24+. No third-party modules.

## Install

```sh
go get github.com/s0x90/zkteco-biota-client/biotime
```

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
	client, err := biotime.New("http://192.168.0.27:8080",
		biotime.WithVersion(biotime.Version8),          // legacy production server
		biotime.WithAuthScheme(biotime.AuthJWT),        // 8.x documents JWT login
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

Login is lazy: the first request obtains a token with the configured
credentials. A request rejected with `401` triggers one re-login and retry,
which keeps long-running programs working across JWT expiry. Call
`client.Login(ctx)` to authenticate eagerly, or pass a pre-issued token with
`biotime.WithToken`.

## Resources

| Service | Endpoint | Operations |
|---|---|---|
| `client.Employees` | `/personnel/api/employees/` | `List`, `All`, `Get`, `GetByCode`, `Create`, `Update`, `Delete` |
| `client.Departments` | `/personnel/api/departments/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Areas` | `/personnel/api/areas/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Positions` | `/personnel/api/positions/` | `List`, `All`, `Get`, `Create`, `Update`, `Delete` |
| `client.Terminals` | `/iclock/api/terminals/` | `List`, `All`, `Get` |
| `client.Transactions` | `/iclock/api/transactions/` | `List`, `All`, `Get` |

`List` returns one `Page[T]` (with `Count` and `HasNext()`); `All` returns an
`iter.Seq2[T, error]` that follows the server's `next` links on demand and
stops when you `break`. Only the query parameters of `next` are used, never
its host, so servers behind a proxy that advertise an internal address still
work; a server that keeps advertising a page it already served ends the walk
with an error instead of looping. `biotime.Collect` drains an iterator into a
slice. `Update` sends `PATCH`, so only the fields you set are changed; an
empty, non-nil slice such as `Area: []int{}` is sent and clears the
assignment, a nil one is omitted:

```go
emp, err := client.Employees.Create(ctx, &biotime.EmployeeParams{
	EmpCode:    biotime.Ptr("1042"),
	FirstName:  biotime.Ptr("Harry"),
	LastName:   biotime.Ptr("Potter"),
	Department: biotime.Ptr(1),
	Area:       []int{1},
	HireDate:   biotime.NewDate(time.Now()),
	Extra:      map[string]any{"Passport": "AB123"}, // custom fields defined in the server UI
})

_, err = client.Employees.Update(ctx, emp.ID, &biotime.EmployeeParams{CardNo: biotime.Ptr("5659812")})
```

Custom employee attributes that the administrator added in the server UI are
returned in `Employee.Extra` as raw JSON.

Endpoints the package does not model (schedules, manual logs, device
commands, …) are reachable with `client.Do`, `client.Get` and `client.Post`,
which handle authentication, JSON encoding and error mapping:

```go
var out map[string]any
err := client.Do(ctx, http.MethodPost, "/att/api/manualLogs/", nil, map[string]any{
	"employee": emp.ID, "punch_time": "2024-06-26 09:00:00", "punch_state": 0, "work_code": "", "apply_reason": "forgot card",
}, &out)
```

## Errors

Every non-2xx response, and every 9.0 list response with a non-zero `code`,
is returned as a `*biotime.Error` carrying the status, the server message and
any per-field validation messages. Redirects are not followed and surface as
a `3xx` error, because a followed redirect would turn a `POST` into a `GET`
and silently decode the wrong resource. Rejected credentials match
`ErrUnauthorized` (and `ErrValidation`, which is how the server frames them).
Sentinels work with `errors.Is`:

```go
_, err := client.Employees.Get(ctx, 999)
switch {
case errors.Is(err, biotime.ErrNotFound):
case errors.Is(err, biotime.ErrUnauthorized):
case errors.Is(err, biotime.ErrValidation):
	var apiErr *biotime.Error
	errors.As(err, &apiErr)
	fmt.Println(apiErr.Fields) // map[emp_code:[This field is required.]]
}
```

## Time zones

ZKBio Time stores and returns wall-clock times without zone information. The
client interprets them, and encodes every `DateTime`, `Date` and
`StartTime`/`EndTime` filter it sends, in the zone returned by
`biotime.Location()`, which defaults to `time.Local`. That default is wrong
whenever the program runs in a different zone than the server, which is the
norm in containers (UTC), so call `biotime.SetLocation` once at start-up.
The setting is package wide: one process talks to servers in one zone.

## Walking punches

Pagination is by page number over live data. Punches keep arriving and many
share a `punch_time`, so a walk ordered by `punch_time` alone is not stable
across pages. The filter is sent exactly as written; for a lossless export
order by `"punch_time,id"`, bound the window with an `EndTime` in the past,
and continue from there on the next run. A server that keeps advertising a
page it already served ends the walk with an error, but that page has
already been yielded, so a consumer that writes as it reads should dedupe on
`ID`.

## Other options

| Option | Purpose |
|---|---|
| `WithHTTPClient(*http.Client)` | custom transport, TLS settings, proxies (set `CheckRedirect` to return `http.ErrUseLastResponse`) |
| `WithTimeout(d)` | per-request timeout of the default client (30s) |
| `WithMaxBodySize(n)` | cap on buffered response and reader request bodies (32 MiB) |
| `WithLogger(*slog.Logger)` | debug-log every request (never logs tokens or passwords) |
| `WithUserAgent(s)` | custom `User-Agent` |
| `WithPageSizeParam(name)` | override the page size parameter for non-standard servers |

## Example program

`examples/basic` lists devices, departments, employees and recent punches
from a live server:

```sh
BIOTIME_URL=http://192.168.0.27:8080 BIOTIME_USER=admin BIOTIME_PASS=secret \
  go run ./examples/basic -version 8 -jwt -since 24h
```

## Notes on the 9.0 manual

- The manual documents area updates as `POST /personnel/api/areas/<id>/`; the
  server actually accepts `PATCH` like every other resource, and that is what
  `Areas.Update` uses.
- The manual's area list example shows a bare object; the server returns the
  usual paginated envelope.
