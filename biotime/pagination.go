package biotime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
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
	//
	// Pagination is by page number over live data. Rows inserted while a
	// walk is in progress shift the pages, and rows with equal sort keys
	// have no stable order between requests. For a lossless walk order by a
	// key that is unique and monotonic for the rows in range (e.g. "id") or
	// add it as a tiebreaker (e.g. "punch_time,id"), and bound the query to
	// a closed range in the past.
	Ordering string
	// Search is a free-text term matched case-insensitively against the
	// searchable fields of the resource (the "search" parameter). The typed
	// filter fields, by contrast, match exactly.
	Search string
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
	if o.Search != "" {
		q.Set("search", o.Search)
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
//
// A body that is neither shape is an error rather than an empty page: a
// decoder that reports "I did not recognize this" as "there was nothing
// there" turns a misrouted request or an interposed proxy into an export
// that writes no rows and exits zero.
func (p *Page[T]) UnmarshalJSON(b []byte) error {
	// The scalar members are pointers because their presence, not their
	// value, is what distinguishes a list response from any other object.
	var raw struct {
		Count    *FlexInt        `json:"count"`
		Next     *string         `json:"next"`
		Previous *string         `json:"previous"`
		Results  []T             `json:"results"`
		Data     json.RawMessage `json:"data"`
		Code     *FlexInt        `json:"code"`
		Msg      *string         `json:"msg"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = Page[T]{}
	if raw.Count != nil {
		p.Count = int(*raw.Count)
	}
	if raw.Code != nil {
		p.Code = int(*raw.Code)
	}
	if raw.Msg != nil {
		p.Msg = *raw.Msg
	}
	if raw.Next != nil {
		p.Next = *raw.Next
	}
	if raw.Previous != nil {
		p.Previous = *raw.Previous
	}

	data := bytes.TrimSpace(raw.Data)
	switch {
	case raw.Results != nil:
		p.Results = raw.Results

	case len(data) > 0 && !bytes.Equal(data, []byte("null")):
		if data[0] != '[' {
			return fmt.Errorf("biotime: list response carries %s as its \"data\" member, not a list", jsonKind(data))
		}
		if err := json.Unmarshal(data, &p.Results); err != nil {
			return err
		}

	case len(raw.Data) == 0 && raw.Count == nil && raw.Next == nil &&
		raw.Previous == nil && raw.Code == nil && raw.Msg == nil:
		// Not a list response at all: no member of either envelope is
		// present. A failure envelope carries "code" and reaches the
		// caller as an *Error, so it must not be caught here.
		return errors.New("biotime: response is not a list: it has no count, results or data member")
	}
	if p.Results == nil {
		p.Results = []T{}
	}
	return nil
}

// jsonKind names the JSON type of a value for an error message. The value
// itself is never quoted: a response body holds personal data.
func jsonKind(b []byte) string {
	switch b[0] {
	case '{':
		return "an object"
	case '[':
		return "a list"
	case '"':
		return "a string"
	case 't', 'f':
		return "a boolean"
	default:
		return "a number"
	}
}

func (p *Page[T]) envelopeCode() int { return p.Code }

// listPage fetches a single page from a collection endpoint.
func listPage[T any](ctx context.Context, c *Client, path string, q url.Values) (*Page[T], error) {
	var page Page[T]
	if err := c.request(ctx, http.MethodGet, path, q, nil, &page, true); err != nil {
		return nil, err
	}
	return &page, nil
}

// iterate walks a collection starting at the page selected by q and yields
// every object. Subsequent pages are requested with the query parameters of
// the server's "next" link, so the walk works whether the server paginates
// by page number or by offset. Iteration stops at the first error, which is
// yielded with a zero value.
//
// A server that keeps advertising a page it has already served ends the walk
// with an error rather than looping forever. The repeated page has already
// been yielded by then; consumers that write as they read should dedupe on
// identifier or buffer a page before committing. A page that carries a next
// link but no rows is reported as an error too: a walk that ends early
// without one would look like a complete export.
//
// The returned sequence can be ranged over any number of times, and
// concurrently; every walk starts from the first page.
func iterate[T any](ctx context.Context, c *Client, path string, q url.Values) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		fail := func(err error) {
			var zero T
			yield(zero, err)
		}
		// Per-walk state: the captured q is never written.
		cur := url.Values{}
		if q != nil {
			cur = maps.Clone(q)
		}
		seen := map[string]struct{}{cur.Encode(): {}}
		for {
			p, err := listPage[T](ctx, c, path, cur)
			if err != nil {
				fail(err)
				return
			}
			for _, item := range p.Results {
				if !yield(item, nil) {
					return
				}
			}
			if !p.HasNext() {
				return
			}
			if len(p.Results) == 0 {
				fail(fmt.Errorf("biotime: server returned an empty page that links to %s, aborting iteration", pageRef(p.Next)))
				return
			}
			next, err := nextQuery(cur, p.Next)
			if err != nil {
				fail(err)
				return
			}
			key := next.Encode()
			if _, dup := seen[key]; dup {
				fail(fmt.Errorf("biotime: server repeated %s, aborting iteration", pageRef(p.Next)))
				return
			}
			seen[key] = struct{}{}
			cur = next
		}
	}
}

// pageRef names the page a next link points to for an error message. The
// link's query carries the filter, and filter values such as names do not
// belong in an error string; the page number is what an operator needs.
func pageRef(next string) string {
	u, err := url.Parse(next)
	if err != nil {
		return "the next page"
	}
	// The link is server-controlled input headed for a log line; only a
	// number gets through.
	q := u.Query()
	if page := q.Get("page"); page != "" {
		if _, err := strconv.Atoi(page); err == nil {
			return "page " + page
		}
		return "a page with a non-numeric number"
	}
	if offset := q.Get("offset"); offset != "" {
		if _, err := strconv.Atoi(offset); err == nil {
			return "the page at offset " + offset
		}
	}
	return "the next page"
}

// nextQuery derives the query for the following page from the server's
// "next" link. Only the link's query parameters are used, overlaid on the
// current ones: servers behind a proxy advertise internal addresses in the
// link, and the paging parameters are all that differs between pages.
func nextQuery(cur url.Values, next string) (url.Values, error) {
	u, err := url.Parse(next)
	if err != nil {
		return nil, fmt.Errorf("biotime: invalid next link %q: %w", next, err)
	}
	q := maps.Clone(cur)
	maps.Copy(q, u.Query())
	return q, nil
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

func (q query) time(key string, val time.Time) {
	if !val.IsZero() {
		q.Set(key, val.In(Location()).Format(DateTimeLayout))
	}
}

// buildQuery assembles the query of a list call: paging and ordering from o,
// resource specific filters from fill, and raw params last so that callers
// can override anything.
func buildQuery(o ListOptions, params map[string]string, pageSizeParam string, fill func(q query)) url.Values {
	q := newQuery()
	o.apply(q.Values, pageSizeParam)
	fill(q)
	for k, v := range params {
		q.Set(k, v)
	}
	return q.Values
}
