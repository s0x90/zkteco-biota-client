# Security policy

## Reporting a vulnerability

Please report security issues privately, not as a public issue.

Use GitHub's private reporting form on this repository: **Security → Report
a vulnerability**. It opens a draft advisory visible only to you and the
maintainers.

Please include:

- what an attacker can do, and what they need in order to do it;
- the affected version or commit;
- steps to reproduce, with credentials, server addresses and personal data
  removed.

You will get an acknowledgement within a week. Once a fix is ready, the
advisory is published together with the release that carries it, crediting
you unless you would rather stay anonymous.

## Scope

This is a client library for a server nobody here operates. In scope are
defects in this code: credentials or personal data reaching a log or an
error message, a response from the server driving the client into
unintended behavior, a dependency of the lint tooling in
`internal/tools/go.mod`.

Out of scope are vulnerabilities in ZKTeco's ZKBio Time or BioTime servers
themselves, and in the devices they talk to. Report those to ZKTeco.

## Supported versions

The latest tagged release and the `master` branch. There is no backporting
to earlier tags.

## What this library does with credentials

Reviewing a change, or an integration built on this library, is easier with
the guarantees it tries to keep:

- The API token and the configured password are never written to a log or
  an error message.
- No error message or log line renders a request's query string, so the
  filter values a list call sends, employee names and codes among them, do
  not reach either. The complete address stays on `Error.URL` for callers
  who want it, so log that field only where the query is safe in your logs.
  An identifier passed directly to a lookup is echoed back in its own
  error, for example the code given to `GetByCode`: an error names what it
  could not find.
- A base URL carrying credentials is refused at construction, because it
  would be rendered wherever the address is printed.
- A device PIN returned by the server is held in a `Secret`, which prints
  as `[redacted]` through `fmt`, `slog` and `encoding/json`.

A change that weakens any of these is a security regression even when no
test fails.
