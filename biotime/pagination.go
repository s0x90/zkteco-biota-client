package biotime

import (
	"context"
	"encoding/json"
	"iter"
	"net/url"
	"strconv"
	"time"
)

// ListOptions carries the pagination and ordering parameters shared by every
// list endpoint. Embed it in a filter and leave fields zero for the server
// defaults.
type ListOptions struct {
	// Page is the 1-based page number.
	Page int
	// PageSize is the number of objects per page. The server caps large
	// values; the client sends it as "page_size" (8.x) or "limit" (9.0).
	PageSize int
	// Ordering is a comma separated list of fields to sort by. Prefix a
	// field with "-" for descending order, e.g. "-punch_time".
	Ordering string
}

func (o ListOptions) apply(q url.Values, pageSizeParam string) {
	if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	if o.PageSize > 0 {
		q.Set(pageSizeParam, strconv.Itoa(o.PageSize))
	}
	if o.Ordering != "" {
		q.Set("ordering", o.Ordering)
	}
}

// Page is one page of a list response. It decodes both the 8.x shape
// ({count,next,previous,results}) and the 9.0 shape
// ({count,next,previous,code,msg,data}).
type Page[T any] struct {
	// Count is the total number of objects matching the query.
	Count int
	// Next and Previous are the URLs of the adjacent pages, or "" at the ends.
	Next     string
	Previous string
	// Results holds the objects on this page.
	Results []T
	// Code and Msg carry the 9.0 application status. Code is 0 on success.
	Code int
	Msg  string
}

// HasNext reports whether another page follows this one.
func (p *Page[T]) HasNext() bool { return p.Next != "" }

// UnmarshalJSON implements [json.Unmarshaler].
func (p *Page[T]) UnmarshalJSON(b []byte) error {
	var raw struct {
		Count    FlexInt         `json:"count"`
		Next     *string         `json:"next"`
		Previous *string         `json:"previous"`
		Results  []T             `json:"results"`
		Data     json.RawMessage `json:"data"`
		Code     FlexInt         `json:"code"`
		Msg      string          `json:"msg"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = Page[T]{
		Count: int(raw.Count),
		Code:  int(raw.Code),
		Msg:   raw.Msg,
	}
	if raw.Next != nil {
		p.Next = *raw.Next
	}
	if raw.Previous != nil {
		p.Previous = *raw.Previous
	}
	switch {
	case raw.Results != nil:
		p.Results = raw.Results
	case len(raw.Data) > 0 && raw.Data[0] == '[':
		if err := json.Unmarshal(raw.Data, &p.Results); err != nil {
			return err
		}
	}
	if p.Results == nil {
		p.Results = []T{}
	}
	return nil
}

func (p *Page[T]) envelopeCode() int { return p.Code }

// listPage fetches a single page from a collection endpoint.
func listPage[T any](ctx context.Context, c *Client, path string, q url.Values) (*Page[T], error) {
	var page Page[T]
	if err := c.request(ctx, "GET", path, q, nil, &page, true); err != nil {
		return nil, err
	}
	return &page, nil
}

// iterate walks a collection page by page starting at the page named in q
// (or 1) and yields every object. Iteration stops at the first error, which
// is yielded with a zero value.
func iterate[T any](ctx context.Context, c *Client, path string, q url.Values) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		q = cloneValues(q)
		page := 1
		if v := q.Get("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				page = n
			}
		}
		for {
			q.Set("page", strconv.Itoa(page))
			p, err := listPage[T](ctx, c, path, q)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range p.Results {
				if !yield(item, nil) {
					return
				}
			}
			if !p.HasNext() || len(p.Results) == 0 {
				return
			}
			page++
		}
	}
}

// Collect drains an iterator produced by one of the All methods into a slice.
// It stops at the first error and returns the objects gathered so far.
func Collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for item, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}

func cloneValues(q url.Values) url.Values {
	out := make(url.Values, len(q)+1)
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// query is a small builder for filter parameters that skips zero values.
type query struct {
	url.Values
}

func newQuery() query { return query{Values: url.Values{}} }

func (q query) str(key, val string) {
	if val != "" {
		q.Set(key, val)
	}
}

func (q query) int(key string, val int) {
	if val != 0 {
		q.Set(key, strconv.Itoa(val))
	}
}

func (q query) intPtr(key string, val *int) {
	if val != nil {
		q.Set(key, strconv.Itoa(*val))
	}
}

func (q query) boolPtr(key string, val *bool) {
	if val != nil {
		q.Set(key, strconv.FormatBool(*val))
	}
}

func (q query) time(key string, val time.Time) {
	if !val.IsZero() {
		q.Set(key, val.In(Location()).Format(DateTimeLayout))
	}
}

func (q query) date(key string, val time.Time) {
	if !val.IsZero() {
		q.Set(key, val.In(Location()).Format(DateLayout))
	}
}
