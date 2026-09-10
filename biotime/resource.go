package biotime

import (
	"context"
	"iter"
	"net/http"
	"net/url"
)

// queryFilter is implemented by the per-resource *Filter types. A nil
// pointer must yield an empty query.
type queryFilter interface {
	values(pageSizeParam string) url.Values
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
	return listPage[T](ctx, r.c, r.path, filter.values(r.c.pageSizeParam))
}

// All iterates over every object matching filter, fetching pages on demand
// by following the server's "next" links. Iteration stops at the first
// error, which is yielded with a zero value.
func (r *collection[T, F]) All(ctx context.Context, filter F) iter.Seq2[T, error] {
	return iterate[T](ctx, r.c, r.path, filter.values(r.c.pageSizeParam))
}

// Get returns the object with the given identifier.
func (r *collection[T, F]) Get(ctx context.Context, id int) (*T, error) {
	var v T
	if err := r.c.Get(ctx, detailPath(r.path, id), nil, &v); err != nil {
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
	var v T
	if err := r.c.Post(ctx, r.path, params, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Update changes the provided fields of an object (HTTP PATCH).
func (r *resource[T, P, F]) Update(ctx context.Context, id int, params *P) (*T, error) {
	var v T
	if err := r.c.Do(ctx, http.MethodPatch, detailPath(r.path, id), nil, params, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Delete removes an object.
func (r *resource[T, P, F]) Delete(ctx context.Context, id int) error {
	return r.c.Do(ctx, http.MethodDelete, detailPath(r.path, id), nil, nil, nil)
}
