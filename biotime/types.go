package biotime

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// FlexInt is an integer that also accepts JSON numeric strings when decoding.
// Use a pointer to distinguish null from zero.
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
		f, ferr := n.Float64()
		if ferr != nil {
			return fmt.Errorf("biotime: FlexInt: %w", err)
		}
		v = int64(f)
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
		var head struct {
			ID FlexInt `json:"id"`
		}
		if err := json.Unmarshal(b, &head); err != nil {
			return err
		}
		*r = Ref[T]{ID: int(head.ID), Object: &obj}
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
// administrators can add in the server UI.
func extraFields(b []byte, t reflect.Type) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	known := jsonKeys(t)
	for k := range all {
		if _, ok := known[k]; ok {
			delete(all, k)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// mergeExtra encodes v as a JSON object and adds the members of extra that
// v does not already define.
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
			continue
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("biotime: extra field %q: %w", k, err)
		}
		obj[k] = raw
	}
	return json.Marshal(obj)
}
