package biotime

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Sentinel errors that can be matched with [errors.Is] against an [*Error].
var (
	// ErrUnauthorized is matched when the server answered 401 or 403, and
	// when a login attempt was rejected.
	ErrUnauthorized = errors.New("biotime: unauthorized")
	// ErrNotFound is matched when the server answered 404.
	ErrNotFound = errors.New("biotime: not found")
	// ErrValidation is matched when the server answered 400: the request
	// is malformed and repeating it cannot succeed. [Error.Fields] carries
	// the per-field messages when the server sent any.
	ErrValidation = errors.New("biotime: validation failed")
	// ErrNoCredentials is returned when a request needs a token but neither
	// [WithToken] nor [WithCredentials] were configured.
	ErrNoCredentials = errors.New("biotime: no token or credentials configured")
	// ErrUnsupportedField is matched by an [*UnsupportedFieldError] with a
	// definitive verdict: the server accepted a write with 2xx but the
	// record shows that a field of the request was not applied, which
	// Django REST framework does silently for members it does not know.
	ErrUnsupportedField = errors.New("biotime: server ignored a request field")
	// ErrUnverified is matched by an [*UnsupportedFieldError] without a
	// verdict: the server accepted the write, but the record could not be
	// read back, so the requested fields are neither confirmed nor refuted.
	// Read the record again; do not repeat the write.
	ErrUnverified = errors.New("biotime: write accepted, fields unverified")
)

// FieldVerdict is the conclusion an [*UnsupportedFieldError] reports.
type FieldVerdict string

// Verdicts of the read-back after a write.
const (
	// VerdictIgnored: the record carries a different value than requested.
	VerdictIgnored FieldVerdict = "ignored by the server"
	// VerdictNotReported: the record does not carry the setting at all, not
	// even on the detail view.
	VerdictNotReported FieldVerdict = "not reported by the server"
	// VerdictUnverified: the read-back failed; see
	// [UnsupportedFieldError.Cause].
	VerdictUnverified FieldVerdict = "unverified"
	// VerdictNoID: the write response carried no identifier, so there was
	// nothing to read back.
	VerdictNoID FieldVerdict = "write response carried no id"
)

// definitive reports whether the verdict refutes the write, as opposed to
// leaving it unknown.
func (v FieldVerdict) definitive() bool {
	return v == VerdictIgnored || v == VerdictNotReported
}

// UnsupportedFieldError reports the outcome of a write the server accepted
// whose requested fields could not all be confirmed on the record. Employee
// is the record as the server holds it after the write; for a create it
// exists on the server, and correcting or removing it is the caller's
// decision, the client never deletes on its own.
//
// A definitive verdict ([VerdictIgnored], [VerdictNotReported]) matches
// [ErrUnsupportedField] with [errors.Is]. The others ([VerdictUnverified],
// [VerdictNoID]) match [ErrUnverified]; the fields are then neither
// confirmed nor refuted, Cause is reachable with [errors.Is] and
// [errors.As] through Unwrap, and the right reaction is to read the record
// again, not to repeat the write.
type UnsupportedFieldError struct {
	// Field is the JSON name of the request member the verdict is about;
	// empty when the verdict is not about a single field.
	Field string
	// Reason is the verdict.
	Reason   FieldVerdict
	Employee *Employee
	// Cause is the error of the read-back, when that is what failed.
	Cause error
}

// Error implements the error interface.
func (e *UnsupportedFieldError) Error() string {
	head := ErrUnsupportedField
	if !e.Reason.definitive() {
		head = ErrUnverified
	}
	id := "unknown"
	if e.Employee != nil {
		id = strconv.Itoa(e.Employee.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%v: %s (employee %s)", head, e.Reason, id)
	if e.Field != "" {
		fmt.Fprintf(&b, " field %s", e.Field)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, ": %v", e.Cause)
	}
	return b.String()
}

// Is reports whether target is the sentinel for the verdict:
// [ErrUnsupportedField] for a definitive one, [ErrUnverified] otherwise.
func (e *UnsupportedFieldError) Is(target error) bool {
	switch target {
	case ErrUnsupportedField:
		return e.Reason.definitive()
	case ErrUnverified:
		return !e.Reason.definitive()
	default:
		return false
	}
}

// Unwrap returns Cause.
func (e *UnsupportedFieldError) Unwrap() error { return e.Cause }

// Error describes a failed API call. It is returned for any non-2xx response
// and for 9.0 list responses whose envelope carries a non-zero code.
type Error struct {
	// StatusCode is the HTTP status of the response.
	StatusCode int
	// Method and URL identify the request that failed. URL is complete,
	// query included; [Error.Error] omits the query because filter values
	// such as names and employee codes do not belong in a log line.
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
	endpoint, _, _ := strings.Cut(e.URL, "?")
	fmt.Fprintf(&b, "biotime: %s %s: %d %s", e.Method, endpoint, e.StatusCode, http.StatusText(e.StatusCode))
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
		return e.StatusCode == http.StatusBadRequest
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
