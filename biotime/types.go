package biotime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Layouts used by the server for naive (zone-less) timestamps.
const (
	DateTimeLayout = "2006-01-02 15:04:05"
	DateLayout     = "2006-01-02"
)

// location is the zone applied when decoding naive timestamps and when
// encoding them, in filters and in request bodies alike. Decoding happens in
// [json.Unmarshaler] implementations that have no access to a client, so the
// setting is package wide. See [SetLocation]. A nil value means [time.Local].
var location atomic.Pointer[time.Location]

// Location returns the zone used to interpret the server's naive timestamps.
// The default is [time.Local].
func Location() *time.Location {
	if l := location.Load(); l != nil {
		return l
	}
	return time.Local
}

// SetLocation sets the zone used to interpret the naive (zone-less)
// timestamps the server returns and to format the ones the client sends.
// ZKBio Time stores wall-clock times without zone information, so this must
// match the server's zone; the default of [time.Local] is wrong whenever the
// program runs in a different zone than the server, which is the norm in
// containers. Passing nil restores [time.Local].
//
// A zone that observes daylight saving cannot express every timestamp the
// server may send: the hour a transition skips does not exist, and the hour
// it repeats is ambiguous. Neither is reported as an error; see parseTime
// for what happens instead. A server kept in UTC, or any zone without
// daylight saving, has neither problem.
func SetLocation(loc *time.Location) { location.Store(loc) }

// dateTimeLayouts lists the timestamp formats observed across server
// generations, most common first.
var dateTimeLayouts = []string{
	DateTimeLayout,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999",
	"2006-01-02T15:04:05.999999",
	time.RFC3339Nano,
	time.RFC3339,
	DateLayout,
}

// DateTime is a timestamp encoded as "2006-01-02 15:04:05" in the server's
// zone (see [Location]); the instant is converted to that zone on encoding.
// A JSON null or empty string decodes to the zero value, and the zero value
// encodes as null.
type DateTime struct {
	time.Time
}

// NewDateTime wraps t.
func NewDateTime(t time.Time) DateTime { return DateTime{Time: t} }

// String formats the value with [DateTimeLayout] in the zone returned by
// [Location].
func (d DateTime) String() string {
	if d.IsZero() {
		return ""
	}
	return d.In(Location()).Format(DateTimeLayout)
}

// MarshalJSON implements [json.Marshaler].
func (d DateTime) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *DateTime) UnmarshalJSON(b []byte) error {
	s, ok, err := jsonString(b)
	if err != nil {
		return fmt.Errorf("biotime: DateTime: %w", err)
	}
	if !ok {
		d.Time = time.Time{}
		return nil
	}
	t, err := parseTime(s, dateTimeLayouts)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// Date is a calendar date encoded as "2006-01-02". A JSON null or empty
// string decodes to the zero value, and the zero value encodes as null.
// Construct values with [NewDate] so that the date is taken in the server's
// zone.
type Date struct {
	time.Time
}

// NewDate returns the calendar date of the instant t in the zone returned by
// [Location], which is the date the server would record for it.
func NewDate(t time.Time) Date {
	loc := Location()
	y, m, d := t.In(loc).Date()
	return Date{Time: time.Date(y, m, d, 0, 0, 0, 0, loc)}
}

// String formats the value with [DateLayout].
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Format(DateLayout)
}

// MarshalJSON implements [json.Marshaler].
func (d Date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *Date) UnmarshalJSON(b []byte) error {
	s, ok, err := jsonString(b)
	if err != nil {
		return fmt.Errorf("biotime: Date: %w", err)
	}
	if !ok {
		d.Time = time.Time{}
		return nil
	}
	t, err := parseTime(s, []string{DateLayout, DateTimeLayout, "2006-01-02T15:04:05", time.RFC3339})
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// parseTime resolves a naive timestamp against [Location].
//
// Two wall-clock times per year cannot be resolved from the input alone,
// and neither is reported as an error: one inside the hour a daylight
// saving transition skips does not exist, and [time.ParseInLocation]
// normalizes it to a neighboring instant, so it does not round-trip; one
// inside the hour a transition repeats exists twice, and resolves to the
// earlier of the two. Run the server in a zone without daylight saving to
// avoid both. See [SetLocation].
func parseTime(s string, layouts []string) (time.Time, error) {
	loc := Location()
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("biotime: cannot parse time %q", s)
}

// jsonString decodes a JSON string. ok is false for null or "".
func jsonString(b []byte) (s string, ok bool, err error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return "", false, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", false, err
	}
	return s, s != "", nil
}

// FlexString is a string that also accepts JSON numbers, booleans and null
// when decoding. The server is inconsistent about the encoding of values such
// as card numbers and device states across versions and endpoints.
type FlexString string

// UnmarshalJSON implements [json.Unmarshaler].
func (s *FlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = FlexString(str)
		return nil
	}
	*s = FlexString(b)
	return nil
}

// String returns the underlying string.
func (s FlexString) String() string { return string(s) }

// redacted is what a [Secret] encodes as everywhere except [Secret.Value].
const redacted = "[redacted]"

// Secret is a credential the server returns in clear text, such as a device
// PIN. It encodes as "[redacted]" with the fmt verbs, with [slog] and with
// [encoding/json], so that neither a debug print nor a structured log line
// holding the enclosing record leaks it. The records of this package are
// read models, not a storage or migration format: a JSON dump of an
// [Employee] does not carry the credential, and decoding the placeholder
// back is an error rather than a value that could be written to a device.
// Where the clear text is needed, read it deliberately with [Secret.Value].
// Decoding accepts the same inputs as [FlexString].
type Secret string

// Value returns the clear-text credential.
func (s Secret) Value() string { return string(s) }

// String implements [fmt.Stringer] and redacts the value.
func (s Secret) String() string { return redacted }

// GoString implements [fmt.GoStringer] and redacts the value.
func (s Secret) GoString() string { return redacted }

// LogValue implements [slog.LogValuer] and redacts the value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements [json.Marshaler] and redacts the value.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// UnmarshalJSON implements [json.Unmarshaler]. The redaction placeholder is
// rejected, so that a record serialized by this package cannot be fed back
// as a credential.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var f FlexString
	if err := f.UnmarshalJSON(b); err != nil {
		return err
	}
	if string(f) == redacted {
		return fmt.Errorf("biotime: Secret: %q is the redaction placeholder, not a value; records serialized by this package do not carry credentials", redacted)
	}
	*s = Secret(f)
	return nil
}

// FlexInt is an integer that also accepts JSON numeric strings when decoding.
// Use a pointer to distinguish null from zero. A value with a fractional
// part or outside the int64 range is an error, never silently truncated.
type FlexInt int

// UnmarshalJSON implements [json.Unmarshaler].
func (i *FlexInt) UnmarshalJSON(b []byte) error {
	n, ok, err := flexNumber(b)
	if err != nil {
		return err
	}
	if !ok {
		*i = 0
		return nil
	}
	v, err := n.Int64()
	if err != nil {
		// "5.0" and "1e3" are integers written the long way; accept those
		// and nothing else.
		f, ferr := n.Float64()
		if ferr != nil || f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return fmt.Errorf("biotime: FlexInt: %q is not an integer", string(n))
		}
		v = int64(f)
	}
	// int is 32 bits wide on some of the hosts this runs on (a 32-bit
	// Raspberry Pi next to the door controller); a value that does not fit
	// must not wrap.
	if v < math.MinInt || v > math.MaxInt {
		return fmt.Errorf("biotime: FlexInt: %q does not fit in int", string(n))
	}
	*i = FlexInt(v)
	return nil
}

// FlexFloat is a float that also accepts JSON numeric strings when decoding.
// Use a pointer to distinguish null from zero.
type FlexFloat float64

// UnmarshalJSON implements [json.Unmarshaler].
func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	n, ok, err := flexNumber(b)
	if err != nil {
		return err
	}
	if !ok {
		*f = 0
		return nil
	}
	v, err := n.Float64()
	if err != nil {
		return fmt.Errorf("biotime: FlexFloat: %w", err)
	}
	*f = FlexFloat(v)
	return nil
}

// flexNumber decodes a JSON number, or a string holding one. ok is false for
// null and the empty string.
func flexNumber(b []byte) (n json.Number, ok bool, err error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return "", false, nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return "", false, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return "", false, nil
		}
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return "", false, fmt.Errorf("biotime: %q is not a number", s)
		}
		return json.Number(s), true, nil
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return "", false, err
	}
	return n, true, nil
}

// Ref is a reference to a related object. The server returns related objects
// either expanded (as a nested object) or as a bare integer identifier
// depending on the endpoint and server generation; Ref decodes both. When
// encoding, only the identifier is written, which is what write endpoints
// expect.
type Ref[T any] struct {
	// ID is the identifier of the referenced object. Zero means null.
	ID int
	// Object holds the expanded object when the server returned one.
	Object *T
}

// RefID returns a reference to the object with the given identifier.
func RefID[T any](id int) Ref[T] { return Ref[T]{ID: id} }

// IsZero reports whether the reference is null.
func (r Ref[T]) IsZero() bool { return r.ID == 0 && r.Object == nil }

// MarshalJSON implements [json.Marshaler].
func (r Ref[T]) MarshalJSON() ([]byte, error) {
	if r.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(r.ID)
}

// UnmarshalJSON implements [json.Unmarshaler].
func (r *Ref[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
		*r = Ref[T]{}
		return nil
	case b[0] == '{':
		var obj T
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		// b is valid JSON by now; pick the identifier out of it without a
		// second decode of the whole object.
		var id FlexInt
		err := objectMembers(b, func(key, value []byte) error {
			if string(key) == "id" {
				return id.UnmarshalJSON(value)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("biotime: Ref: %w", err)
		}
		*r = Ref[T]{ID: int(id), Object: &obj}
		return nil
	default:
		var id FlexInt
		if err := id.UnmarshalJSON(b); err != nil {
			return fmt.Errorf("biotime: Ref: %w", err)
		}
		*r = Ref[T]{ID: int(id)}
		return nil
	}
}

// knownKeys caches the set of JSON keys declared by a struct type.
var knownKeys sync.Map // reflect.Type -> map[string]struct{}

// jsonKeys returns the JSON object keys produced by the exported fields of the
// struct type t, following embedded structs.
func jsonKeys(t reflect.Type) map[string]struct{} {
	if v, ok := knownKeys.Load(t); ok {
		return v.(map[string]struct{})
	}
	keys := make(map[string]struct{})
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for f := range t.Fields() {
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}
			if !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			keys[name] = struct{}{}
		}
	}
	walk(t)
	knownKeys.Store(t, keys)
	return keys
}

// extraFields returns the members of the JSON object b that are not declared
// by the struct type t. It is used to surface the custom employee attributes
// administrators can add in the server UI. b must already have been decoded
// into t, which proves it well formed; the members are then located with
// [objectMembers] rather than a second full decode, which on a 5000-row page
// would double the time and allocations of every list call.
func extraFields(b []byte, t reflect.Type) (map[string]json.RawMessage, error) {
	known := jsonKeys(t)
	var extra map[string]json.RawMessage
	err := objectMembers(b, func(key, value []byte) error {
		// The conversion in an index expression does not allocate, so the
		// common case, a known key, costs nothing.
		if _, ok := known[string(key)]; ok {
			return nil
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage)
		}
		// The input belongs to the caller; json.Unmarshaler implementations
		// must copy what they retain.
		extra[string(key)] = bytes.Clone(value)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return extra, nil
}

// errMalformedObject is returned by objectMembers for input that is not a
// JSON object. Callers decode the input first, so reaching it means the
// scanner and encoding/json disagree, which is a bug worth a clear name.
var errMalformedObject = errors.New("biotime: malformed JSON object")

// objectMembers calls fn with the key and the raw value of every member of
// the JSON object b, in order. Keys are unescaped; keys without escapes and
// all values are sub-slices of b, trimmed of surrounding whitespace, and
// must be copied to be retained.
// The scan is structural only (strings, nesting, separators) and relies on b
// being valid JSON; it never panics on invalid input but may report it as
// errMalformedObject rather than pinpoint it.
func objectMembers(b []byte, fn func(key, value []byte) error) error {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return errMalformedObject
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return nil
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return errMalformedObject
		}
		keyEnd, ok := stringEnd(b, i)
		if !ok {
			return errMalformedObject
		}
		key, err := unquote(b[i:keyEnd])
		if err != nil {
			return err
		}
		i = skipSpace(b, keyEnd)
		if i >= len(b) || b[i] != ':' {
			return errMalformedObject
		}
		i = skipSpace(b, i+1)
		valueEnd, ok := valueEnd(b, i)
		if !ok {
			return errMalformedObject
		}
		if err := fn(key, b[i:valueEnd]); err != nil {
			return err
		}
		i = skipSpace(b, valueEnd)
		if i >= len(b) {
			return errMalformedObject
		}
		switch b[i] {
		case ',':
			i = skipSpace(b, i+1)
		case '}':
			return nil
		default:
			return errMalformedObject
		}
	}
}

// skipSpace returns the index of the first byte at or after i that is not
// JSON whitespace.
func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// stringEnd returns the index just past the closing quote of the JSON string
// that opens at b[i].
func stringEnd(b []byte, i int) (end int, ok bool) {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

// valueEnd returns the index just past the JSON value that starts at b[i].
func valueEnd(b []byte, i int) (end int, ok bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				next, ok := stringEnd(b, j)
				if !ok {
					return 0, false
				}
				j = next - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default:
		// A number, true, false or null: runs until a separator.
		for j := i; j < len(b); j++ {
			switch b[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j, j > i
			}
		}
		return len(b), len(b) > i
	}
}

// unquote decodes a JSON string literal. Keys without escapes, which is all
// of them in practice, are returned as a sub-slice of lit without copying.
func unquote(lit []byte) ([]byte, error) {
	if bytes.IndexByte(lit, '\\') < 0 {
		return lit[1 : len(lit)-1], nil
	}
	var s string
	if err := json.Unmarshal(lit, &s); err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// mergeExtra encodes v as a JSON object and adds the members of extra. A key
// that v already encodes is an error: silently keeping the struct's value
// would drop a field the caller meant to send, which is the failure this
// package goes out of its way to report when the server does it.
func mergeExtra(v any, extra map[string]any) ([]byte, error) {
	base, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return base, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	for k, val := range extra {
		if _, exists := obj[k]; exists {
			return nil, fmt.Errorf("biotime: extra field %q is also set on the params struct; set it in one place only", k)
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("biotime: extra field %q: %w", k, err)
		}
		obj[k] = raw
	}
	return json.Marshal(obj)
}
