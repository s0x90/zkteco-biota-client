package biotime

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Sentinel errors that can be matched with [errors.Is] against an [*Error].
var (
	// ErrUnauthorized is matched when the server answered 401 or 403, and
	// when a login attempt was rejected.
	ErrUnauthorized = errors.New("biotime: unauthorized")
	// ErrNotFound is matched when the server answered 404.
	ErrNotFound = errors.New("biotime: not found")
	// ErrValidation is matched when the server answered 400 with field errors.
	ErrValidation = errors.New("biotime: validation failed")
	// ErrNoCredentials is returned when a request needs a token but neither
	// [WithToken] nor [WithCredentials] were configured.
	ErrNoCredentials = errors.New("biotime: no token or credentials configured")
	// ErrUnsupportedField is wrapped by the error returned when the server
	// accepted a write with 2xx but the returned object shows that a field
	// of the request was ignored, which Django REST framework does silently
	// for members it does not know. The object was still written and is
	// returned alongside the error.
	ErrUnsupportedField = errors.New("biotime: server ignored a request field")
)

// Error describes a failed API call. It is returned for any non-2xx response
// and for 9.0 list responses whose envelope carries a non-zero code.
type Error struct {
	// StatusCode is the HTTP status of the response.
	StatusCode int
	// Method and URL identify the request that failed.
	Method string
	URL    string
	// Code is the application-level code from a 9.0 response envelope, if any.
	Code int
	// Message is the server's human readable message ("detail" or "msg").
	Message string
	// Fields holds per-field validation messages from a 400 response.
	Fields map[string][]string
	// Body is the raw response body.
	Body []byte

	// login marks the failure of a login attempt, which the server reports
	// as a 400 validation error but which callers reasonably treat as
	// "unauthorized".
	login bool
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "biotime: %s %s: %d %s", e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Code != 0 {
		fmt.Fprintf(&b, " (code %d)", e.Code)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if len(e.Fields) > 0 {
		keys := slices.Sorted(maps.Keys(e.Fields))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+strings.Join(e.Fields[k], "; "))
		}
		b.WriteString(": ")
		b.WriteString(strings.Join(parts, ", "))
	}
	return b.String()
}

// Is reports whether the error matches one of the package sentinel errors.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.login || e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrValidation:
		return e.StatusCode == http.StatusBadRequest && len(e.Fields) > 0
	default:
		return false
	}
}

// newError builds an [*Error] from a response, extracting the Django REST
// framework error conventions the server uses:
//
//	{"detail": "Not found."}
//	{"emp_code": ["This field is required."], "non_field_errors": ["..."]}
//	{"code": 1, "msg": "..."}
func newError(method, url string, status int, body []byte) *Error {
	e := &Error{StatusCode: status, Method: method, URL: url, Body: body}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		if msg := strings.TrimSpace(string(body)); msg != "" && !strings.HasPrefix(msg, "<") {
			e.Message = msg
		}
		return e
	}

	for key, val := range raw {
		switch key {
		case "detail", "msg", "message":
			var s string
			if json.Unmarshal(val, &s) == nil && s != "" {
				e.Message = s
			}
		case "code":
			var n json.Number
			if json.Unmarshal(val, &n) == nil {
				if i, err := n.Int64(); err == nil {
					e.Code = int(i)
				}
			}
		default:
			if msgs := stringList(val); len(msgs) > 0 {
				if e.Fields == nil {
					e.Fields = make(map[string][]string)
				}
				e.Fields[key] = msgs
			}
		}
	}
	return e
}

// stringList decodes a JSON string, a list of strings, or an object of
// nested field errors into a flat list of messages.
func stringList(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(raw, &nested) == nil {
		var out []string
		for k, v := range nested {
			for _, m := range stringList(v) {
				out = append(out, k+": "+m)
			}
		}
		slices.Sort(out)
		return out
	}
	return nil
}
