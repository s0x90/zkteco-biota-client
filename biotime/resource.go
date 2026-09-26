package biotime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"time"
)

// errNilParams is returned instead of sending the JSON literal null, which
// no endpoint means to accept and which a lenient one answers 2xx to. It is
// refused here, at the one place every write passes through, rather than in
// the services: [EmployeeService.Create] and [EmployeeService.Update] read
// the params again after the write, and a nil that reached them would panic
// with the record already changed.
var errNilParams = errors.New("biotime: nil params")

// queryFilter is implemented by the per-resource *Filter types. A nil
// pointer must yield an empty query.
type queryFilter interface {
	values(queryConfig) url.Values
}

// record is implemented by every type the server identifies. A response
// that decodes without one is not a record, whatever else it holds.
type record interface {
	recordID() int
}

// detail decodes a single-object response the way [Page] decodes a list:
// the 9.0 {code,msg,data} envelope is unwrapped and a non-zero code is
// surfaced, and a body that decodes to a record without an identifier is
// an error rather than an empty value. A decoder that answered a wrapped
// or misrouted response with a zero record would send the caller on to
// update, delete or report employee 0.
type detail[T any] struct {
	V    *T
	code int
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *detail[T]) UnmarshalJSON(b []byte) error {
	var env struct {
		ID   json.RawMessage `json:"id"`
		Code json.RawMessage `json:"code"`
		Msg  *string         `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	// The envelope is recognized by its shape, not by "code" alone: a
	// record may carry custom attributes of these names. A body with an
	// "id" member is a record; so is one whose "code" is not a number, or
	// that has neither "msg" nor "data" beside it.
	var code FlexInt
	if len(env.ID) == 0 && len(env.Code) > 0 && (env.Msg != nil || len(env.Data) > 0) && code.UnmarshalJSON(env.Code) == nil {
		d.code = int(code)
		if d.code != 0 {
			return nil // request turns the code into an *Error
		}
		b = bytes.TrimSpace(env.Data)
		if len(b) == 0 || bytes.Equal(b, []byte("null")) {
			return errors.New("biotime: response envelope carries no data member")
		}
	}
	if err := json.Unmarshal(b, d.V); err != nil {
		return err
	}
	if r, ok := any(d.V).(record); ok && r.recordID() == 0 {
		return errors.New("biotime: response is not a record: it carries no id")
	}
	return nil
}

func (d *detail[T]) envelopeCode() int { return d.code }

func (d *detail[T]) localize(loc *time.Location) {
	if l, ok := any(d.V).(localizable); ok {
		l.localize(loc)
	}
}

// collection is the read side shared by every service: a list endpoint and
// detail lookups by identifier.
type collection[T any, F queryFilter] struct {
	c    *Client
	path string
}

func newCollection[T any, F queryFilter](c *Client, path string) collection[T, F] {
	return collection[T, F]{c: c, path: path}
}

// List returns one page of objects matching filter (nil for all).
func (r *collection[T, F]) List(ctx context.Context, filter F) (*Page[T], error) {
	return listPage[T](ctx, r.c, r.path, filter.values(r.c.queryConfig()))
}

// All iterates over every object matching filter, fetching pages on demand
// by following the server's "next" links. Iteration stops at the first
// error, which is yielded with a zero value.
func (r *collection[T, F]) All(ctx context.Context, filter F) iter.Seq2[T, error] {
	return iterate[T](ctx, r.c, r.path, filter.values(r.c.queryConfig()))
}

// Get returns the object with the given identifier.
func (r *collection[T, F]) Get(ctx context.Context, id int) (*T, error) {
	return r.one(ctx, http.MethodGet, id, nil)
}

// one performs a request against the detail path of id, or the collection
// path when id is zero for a create, and decodes the record it answers.
func (r *collection[T, F]) one(ctx context.Context, method string, id int, body any) (*T, error) {
	path := r.path
	if method != http.MethodPost {
		if id <= 0 {
			return nil, fmt.Errorf("biotime: invalid id %d: identifiers are positive", id)
		}
		path = detailPath(r.path, id)
	}
	var v T
	if err := r.c.Do(ctx, method, path, nil, body, &detail[T]{V: &v}); err != nil {
		return nil, err
	}
	return &v, nil
}

// resource adds the write side for endpoints that accept it.
type resource[T, P any, F queryFilter] struct {
	collection[T, F]
}

func newResource[T, P any, F queryFilter](c *Client, path string) resource[T, P, F] {
	return resource[T, P, F]{collection: newCollection[T, F](c, path)}
}

// Create adds an object. The fields the server requires are documented on
// the params type.
func (r *resource[T, P, F]) Create(ctx context.Context, params *P) (*T, error) {
	if params == nil {
		return nil, errNilParams
	}
	return r.one(ctx, http.MethodPost, 0, params)
}

// Update changes the provided fields of an object (HTTP PATCH).
func (r *resource[T, P, F]) Update(ctx context.Context, id int, params *P) (*T, error) {
	if params == nil {
		return nil, errNilParams
	}
	return r.one(ctx, http.MethodPatch, id, params)
}

// Delete removes an object.
func (r *resource[T, P, F]) Delete(ctx context.Context, id int) error {
	if id <= 0 {
		return fmt.Errorf("biotime: invalid id %d: identifiers are positive", id)
	}
	return r.c.Do(ctx, http.MethodDelete, detailPath(r.path, id), nil, nil, nil)
}
