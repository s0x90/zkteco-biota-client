package biotime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer emulates the subset of ZKBio Time behavior the client relies
// on, for either server generation.
type fakeServer struct {
	t       *testing.T
	version Version
	scheme  AuthScheme
	token   string

	mu       sync.Mutex
	logins   int
	requests []*http.Request
	queries  []url.Values
	bodies   [][]byte
	// reject makes the next n authenticated requests answer 401.
	reject atomic.Int32
	// handler, when set, serves requests after authentication is checked.
	handler http.HandlerFunc
}

func newFakeServer(t *testing.T, version Version, scheme AuthScheme) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{t: t, version: version, scheme: scheme, token: "tok-1"}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.queries = append(f.queries, r.URL.Query())
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()

	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Error(w, "missing trailing slash", http.StatusNotFound)
		return
	}

	if strings.HasSuffix(r.URL.Path, f.scheme.loginPath()) {
		var creds credentials
		if err := json.Unmarshal(body, &creds); err != nil || creds.Username != "admin" || creds.Password != "secret" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"non_field_errors":["Unable to log in with provided credentials."]}`)
			return
		}
		f.mu.Lock()
		f.logins++
		f.token = "tok-" + strconv.Itoa(f.logins)
		tok := f.token
		f.mu.Unlock()
		fmt.Fprintf(w, `{"token":%q}`, tok)
		return
	}

	f.mu.Lock()
	tok := f.token
	f.mu.Unlock()
	if r.Header.Get("Authorization") != string(f.scheme)+" "+tok || f.reject.Load() > 0 {
		if f.reject.Load() > 0 {
			f.reject.Add(-1)
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"detail":"Invalid token."}`)
		return
	}
	if f.handler != nil {
		f.handler(w, r)
		return
	}
	http.NotFound(w, r)
}

// page writes a list response in the fake's server generation.
func (f *fakeServer) page(w http.ResponseWriter, count int, next string, items ...any) {
	var nextVal any
	if next != "" {
		nextVal = next
	}
	env := map[string]any{"count": count, "next": nextVal, "previous": nil}
	if f.version == Version8 {
		env["results"] = items
	} else {
		env["code"] = 0
		env["msg"] = ""
		env["data"] = items
	}
	json.NewEncoder(w).Encode(env)
}

func (f *fakeServer) lastQuery() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[len(f.queries)-1]
}

func (f *fakeServer) lastRequest() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeServer) lastBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1]
}

func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithCredentials("admin", "secret")}, opts...)
	c, err := New(srv.URL, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	for _, bad := range []string{"", "192.168.0.27:8080", "ftp://x", "http://"} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) succeeded", bad)
		}
	}
	if _, err := New("http://x", WithVersion(7)); err == nil {
		t.Error("unsupported version accepted")
	}
	if _, err := New("http://x", WithAuthScheme("Bearer")); err == nil {
		t.Error("unsupported scheme accepted")
	}
	c, err := New("http://x:8080/prefix/", WithVersion(Version8))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "http://x:8080/prefix" || c.Version() != Version8 || c.pageSizeParam != "page_size" {
		t.Errorf("%s %v %s", c.BaseURL(), c.Version(), c.pageSizeParam)
	}
	for _, opts := range [][]Option{
		{WithVersion(Version8), WithPageSizeParam("limit")},
		{WithPageSizeParam("limit"), WithVersion(Version8)},
	} {
		c, _ = New("http://x", opts...)
		if c.pageSizeParam != "limit" {
			t.Error("WithPageSizeParam ignored")
		}
	}
	if _, err := New("http://x", WithMaxBodySize(0)); err == nil {
		t.Error("zero max response size accepted")
	}
	if _, err := New("http://x", WithHTTPClient(nil)); err == nil {
		t.Error("nil http client accepted")
	}
}

// TestEveryServiceReadPath drives List, All and Get of each service through
// the fake server, so that the thin per-service wrappers are all exercised.
func TestEveryServiceReadPath(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/5/") {
			fmt.Fprint(w, `{"id":5}`)
			return
		}
		f.page(w, 1, "", map[string]any{"id": 5})
	}
	c := newTestClient(t, srv)
	ctx := t.Context()

	type readPath struct {
		name string
		list func() (int, error)
		all  func() (int, error)
		get  func() (int, error)
	}
	paths := []readPath{
		{
			"employees",
			func() (int, error) { p, err := c.Employees.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Employees.All(ctx, nil))) },
			func() (int, error) {
				e, err := c.Employees.Get(ctx, 5)
				return objID(e, err, func(e *Employee) int { return e.ID })
			},
		},
		{
			"departments",
			func() (int, error) { p, err := c.Departments.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Departments.All(ctx, nil))) },
			func() (int, error) {
				d, err := c.Departments.Get(ctx, 5)
				return objID(d, err, func(d *Department) int { return d.ID })
			},
		},
		{
			"areas",
			func() (int, error) { p, err := c.Areas.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Areas.All(ctx, nil))) },
			func() (int, error) {
				a, err := c.Areas.Get(ctx, 5)
				return objID(a, err, func(a *Area) int { return a.ID })
			},
		},
		{
			"positions",
			func() (int, error) { p, err := c.Positions.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Positions.All(ctx, nil))) },
			func() (int, error) {
				p, err := c.Positions.Get(ctx, 5)
				return objID(p, err, func(p *Position) int { return p.ID })
			},
		},
		{
			"terminals",
			func() (int, error) { p, err := c.Terminals.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Terminals.All(ctx, nil))) },
			func() (int, error) {
				d, err := c.Terminals.Get(ctx, 5)
				return objID(d, err, func(d *Terminal) int { return d.ID })
			},
		},
		{
			"transactions",
			func() (int, error) { p, err := c.Transactions.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Transactions.All(ctx, nil))) },
			func() (int, error) {
				x, err := c.Transactions.Get(ctx, 5)
				return objID(x, err, func(x *Transaction) int { return x.ID })
			},
		},
	}
	for _, p := range paths {
		for op, call := range map[string]func() (int, error){"List": p.list, "All": p.all, "Get": p.get} {
			if id, err := call(); err != nil || id != 5 {
				t.Errorf("%s.%s: id %d, err %v", p.name, op, id, err)
			}
		}
	}
}

// pageID returns the id of the only object on a page.
func pageID[T any](p *Page[T], err error) (int, error) {
	if err != nil {
		return 0, err
	}
	return firstID(p.Results, nil)
}

// firstID returns the id of the first object of a slice, read through its
// JSON encoding so that the helper does not need one accessor per type.
func firstID[T any](items []T, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	if len(items) != 1 {
		return 0, fmt.Errorf("expected one object, got %d", len(items))
	}
	return objID(&items[0], nil, func(v *T) int {
		b, _ := json.Marshal(v)
		var head struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(b, &head)
		return head.ID
	})
}

func objID[T any](v *T, err error, id func(*T) int) (int, error) {
	if err != nil {
		return 0, err
	}
	return id(v), nil
}

func TestLoginTokenScheme(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)

	if c.Token() != "" {
		t.Fatal("token set before login")
	}
	if _, err := c.Terminals.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if c.Token() != "tok-1" {
		t.Errorf("token %q", c.Token())
	}
	if got := f.lastRequest().Header.Get("Authorization"); got != "Token tok-1" {
		t.Errorf("Authorization %q", got)
	}
	if f.requests[0].URL.Path != "/api-token-auth/" || f.requests[0].Header.Get("Content-Type") != "application/json" {
		t.Errorf("login request %v", f.requests[0])
	}
	if !strings.Contains(string(f.bodies[0]), `"username":"admin"`) {
		t.Errorf("login body %s", f.bodies[0])
	}
}

func TestLoginJWTScheme(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthJWT)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv, WithVersion(Version8), WithAuthScheme(AuthJWT))

	tok, err := c.Login(t.Context())
	if err != nil || tok != "tok-1" {
		t.Fatal(tok, err)
	}
	if f.requests[0].URL.Path != "/jwt-api-token-auth/" {
		t.Errorf("login path %s", f.requests[0].URL.Path)
	}
	if _, err := c.Employees.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Header.Get("Authorization"); got != "JWT tok-1" {
		t.Errorf("Authorization %q", got)
	}
	if f.logins != 1 {
		t.Errorf("logins %d", f.logins)
	}
}

func TestAcceptLanguage(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthJWT)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }

	c := newTestClient(t, srv, WithAuthScheme(AuthJWT))
	if _, err := c.Employees.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	// The login request and the API request both carry the default.
	for _, r := range f.requests {
		if got := r.Header.Get("Accept-Language"); got != "en" {
			t.Errorf("%s Accept-Language %q", r.URL.Path, got)
		}
	}

	c = newTestClient(t, srv, WithAuthScheme(AuthJWT), WithLanguage("ru"))
	if _, err := c.Employees.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Header.Get("Accept-Language"); got != "ru" {
		t.Errorf("Accept-Language %q", got)
	}

	c = newTestClient(t, srv, WithAuthScheme(AuthJWT), WithLanguage(""))
	if _, err := c.Employees.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, set := f.lastRequest().Header["Accept-Language"]; set {
		t.Error("Accept-Language sent although disabled")
	}
}

func TestLoginFailure(t *testing.T) {
	_, srv := newFakeServer(t, Version9, AuthToken)
	c := newTestClient(t, srv, WithCredentials("admin", "wrong"))
	_, err := c.Employees.List(t.Context(), nil)
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.StatusCode != http.StatusBadRequest || !errors.Is(err, ErrValidation) || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "Unable to log in") || strings.Count(err.Error(), "biotime:") != 1 {
		t.Error(err)
	}

	c, _ = New(srv.URL)
	if _, err := c.Employees.List(t.Context(), nil); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("got %v", err)
	}
}

func TestStaticTokenAndReauth(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv, WithToken("tok-1"))

	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 0 {
		t.Errorf("logged in despite static token")
	}

	// Server rotates the token: the next request is rejected once, the
	// client re-authenticates and retries transparently.
	f.mu.Lock()
	f.token = "tok-expired"
	f.mu.Unlock()
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 || c.Token() != "tok-1" {
		t.Errorf("logins %d token %s", f.logins, c.Token())
	}
	if n := len(f.requests); n != 4 {
		t.Errorf("expected 4 requests (list, 401, login, list), got %d", n)
	}

	// Persistent rejection surfaces as ErrUnauthorized after one retry.
	f.reject.Store(10)
	before := len(f.requests)
	_, err := c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests) - before; n != 3 {
		t.Errorf("expected 3 requests (401, login, 401), got %d", n)
	}
}

func TestReauthWithoutCredentials(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	c, _ := New(srv.URL, WithToken("stale"))
	_, err := c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if len(f.requests) != 1 {
		t.Errorf("expected a single request, got %d", len(f.requests))
	}
}

func TestConcurrentLoginHappensOnce(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Positions.List(t.Context(), nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if f.logins != 1 {
		t.Errorf("logins %d", f.logins)
	}
}

func TestPaginationLegacy(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthJWT)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personnel/api/employees/" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			f.page(w, 5, srv.URL+"/personnel/api/employees/?page=2", map[string]any{"id": 1}, map[string]any{"id": 2})
		case "2":
			f.page(w, 5, srv.URL+"/personnel/api/employees/?page=3", map[string]any{"id": 3}, map[string]any{"id": 4})
		case "3":
			f.page(w, 5, "", map[string]any{"id": 5})
		default:
			http.NotFound(w, r)
		}
	}
	c := newTestClient(t, srv, WithVersion(Version8), WithAuthScheme(AuthJWT))

	page, err := c.Employees.List(t.Context(), &EmployeeFilter{
		ListOptions: ListOptions{PageSize: 2, Ordering: "-id", Search: "harry"},
		Department:  3,
		AppStatus:   new(0),
		EmpCode:     "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Count != 5 || len(page.Results) != 2 || !page.HasNext() {
		t.Errorf("%+v", page)
	}
	q := f.lastQuery()
	if q.Get("page_size") != "2" || q.Has("limit") || q.Get("ordering") != "-id" || q.Get("search") != "harry" || q.Get("department") != "3" || q.Get("app_status") != "0" || q.Get("emp_code") != "7" {
		t.Errorf("query %v", q)
	}

	var ids []int
	for e, err := range c.Employees.All(t.Context(), &EmployeeFilter{ListOptions: ListOptions{PageSize: 2}}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	if fmt.Sprint(ids) != "[1 2 3 4 5]" {
		t.Errorf("ids %v", ids)
	}

	// Early break stops fetching.
	before := len(f.requests)
	for e := range c.Employees.All(t.Context(), nil) {
		if e.ID == 1 {
			break
		}
	}
	if n := len(f.requests) - before; n != 1 {
		t.Errorf("expected 1 request after break, got %d", n)
	}

	// Starting page is honored.
	all, err := Collect(c.Employees.All(t.Context(), &EmployeeFilter{ListOptions: ListOptions{Page: 3}}))
	if err != nil || len(all) != 1 || all[0].ID != 5 {
		t.Errorf("%v %v", all, err)
	}
}

func TestPaginationModernAndEnvelopeError(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			f.page(w, 3, srv.URL+"/iclock/api/terminals/?page=2", map[string]any{"id": 1, "sn": "A"}, map[string]any{"id": 2, "sn": "B"})
		case "2":
			f.page(w, 3, "", map[string]any{"id": 3, "sn": "C"})
		default:
			fmt.Fprint(w, `{"count":0,"next":null,"previous":null,"msg":"page out of range","code":2,"data":[]}`)
		}
	}
	c := newTestClient(t, srv)

	page, err := c.Terminals.List(t.Context(), &TerminalFilter{ListOptions: ListOptions{PageSize: 2}, SN: "A", Area: 9, IPAddress: "10.0.0.1", State: new(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 2 || page.Results[1].SN != "B" {
		t.Errorf("%+v", page)
	}
	q := f.lastQuery()
	if q.Get("limit") != "2" || q.Has("page_size") || q.Get("sn") != "A" || q.Get("area") != "9" || q.Get("ip_address") != "10.0.0.1" || q.Get("state") != "1" || q.Has("search") {
		t.Errorf("query %v", q)
	}

	all, err := Collect(c.Terminals.All(t.Context(), nil))
	if err != nil || len(all) != 3 {
		t.Fatalf("%v %v", all, err)
	}

	_, err = c.Terminals.List(t.Context(), &TerminalFilter{ListOptions: ListOptions{Page: 9}})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Code != 2 || apiErr.Message != "page out of range" || apiErr.StatusCode != http.StatusOK {
		t.Fatalf("got %v", err)
	}

	// The iterator surfaces the error and stops.
	var n int
	var iterErr error
	for _, err := range c.Terminals.All(t.Context(), &TerminalFilter{ListOptions: ListOptions{Page: 9}}) {
		n++
		iterErr = err
	}
	if n != 1 || iterErr == nil {
		t.Errorf("n=%d err=%v", n, iterErr)
	}
}

func TestIterationStopsOnEmptyPage(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// A misbehaving server that always claims there is a next page.
		f.page(w, 0, "next")
	}
	c := newTestClient(t, srv)
	all, err := Collect(c.Departments.All(t.Context(), nil))
	if err != nil || len(all) != 0 {
		t.Fatal(all, err)
	}
}

func TestCRUD(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/personnel/api/employees/":
			var in map[string]any
			json.NewDecoder(strings.NewReader(string(f.lastBody()))).Decode(&in)
			if in["emp_code"] != "employee333" || in["department"] != float64(1) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"emp_code":["This field is required."],"area":["This list may not be empty."]}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":4144,"emp_code":"employee333","first_name":"emp3","department":1,"area":[1],"hire_date":"2024-06-26","flow_role":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/personnel/api/employees/4144/":
			fmt.Fprint(w, `{"id":4144,"emp_code":"employee333","department":{"id":1,"dept_code":"1","dept_name":"Department"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/personnel/api/employees/4144/":
			fmt.Fprintf(w, `{"id":4144,"emp_code":"employee333","card_no":"5659812","department":1}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/personnel/api/employees/4144/":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/personnel/api/employees/999/":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"Not found."}`)
		default:
			http.NotFound(w, r)
		}
	}
	c := newTestClient(t, srv)
	ctx := t.Context()

	created, err := c.Employees.Create(ctx, &EmployeeParams{
		EmpCode: new("employee333"), FirstName: new("emp3"), Department: new(1), Area: []int{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 4144 || created.Department.ID != 1 || created.Department.Object != nil || len(created.Area) != 1 {
		t.Errorf("%+v", created)
	}
	if ct := f.lastRequest().Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q", ct)
	}

	_, err = c.Employees.Create(ctx, &EmployeeParams{FirstName: new("x")})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || !errors.Is(err, ErrValidation) {
		t.Fatalf("got %v", err)
	}
	if len(apiErr.Fields) != 2 || apiErr.Fields["area"][0] != "This list may not be empty." {
		t.Errorf("fields %v", apiErr.Fields)
	}
	if want := "area: This list may not be empty., emp_code: This field is required."; !strings.HasSuffix(err.Error(), want) {
		t.Errorf("message %q", err.Error())
	}

	got, err := c.Employees.Get(ctx, 4144)
	if err != nil || got.Department.Object == nil || got.Department.Object.DeptCode != "1" {
		t.Fatalf("%+v %v", got, err)
	}

	updated, err := c.Employees.Update(ctx, 4144, &EmployeeParams{CardNo: new("5659812")})
	if err != nil || updated.CardNo != "5659812" {
		t.Fatalf("%+v %v", updated, err)
	}
	if f.lastRequest().Method != http.MethodPatch || string(f.lastBody()) != `{"card_no":"5659812"}` {
		t.Errorf("%s %s", f.lastRequest().Method, f.lastBody())
	}

	if err := c.Employees.Delete(ctx, 4144); err != nil {
		t.Fatal(err)
	}
	if f.lastRequest().Method != http.MethodDelete {
		t.Error(f.lastRequest().Method)
	}

	_, err = c.Employees.Get(ctx, 999)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "Not found.") {
		t.Errorf("got %v", err)
	}
}

func TestEmployeeFlagWriteIsVerified(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	// mode selects the server behavior: how the write response and the
	// detail view report the attendance flag.
	var mode atomic.Int32
	const (
		ignored       = iota // write response nested and unchanged, detail the same
		echoed               // 8.x: write response carries the flag top level
		silentOK             // 9.0: write response omits the flags, detail shows them applied
		silentWrong          // 9.0: write response omits the flags, detail shows them unchanged
		neverShown           // neither response carries the flags
		readBackFails        // write response omits the flags, detail answers 500
		noID                 // write response carries no id at all
		partialEcho          // write response echoes enable_att only, detail carries all
	)
	var gets atomic.Int32
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		m := int(mode.Load())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/personnel/api/employees/7/":
			gets.Add(1)
			switch m {
			case silentOK:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","attemployee":{"id":7,"enable_attendance":false}}`)
			case neverShown:
				fmt.Fprint(w, `{"id":7,"emp_code":"7"}`)
			case readBackFails:
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"detail":"boom"}`)
			case partialEcho:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","enable_att":false,"enable_overtime":true,"enable_holiday":true}`)
			default:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","attemployee":{"id":7,"enable_attendance":true}}`)
			}
		case r.Method == http.MethodPatch || r.Method == http.MethodPost:
			switch m {
			case echoed:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","enable_att":false}`)
			case partialEcho:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","enable_att":false}`)
			case noID:
				fmt.Fprint(w, `{"emp_code":"7"}`)
			case silentOK, silentWrong, neverShown, readBackFails:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","first_name":"x","area":[1]}`)
			default:
				fmt.Fprint(w, `{"id":7,"emp_code":"7","attemployee":{"id":7,"enable_attendance":true}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}
	c := newTestClient(t, srv)
	params := &EmployeeParams{EnableAtt: new(false)}
	ctx := t.Context()

	// Value present and different: ignored, no extra request.
	e, err := c.Employees.Update(ctx, 7, params)
	var ufe *UnsupportedFieldError
	if e != nil || !errors.Is(err, ErrUnsupportedField) || !errors.As(err, &ufe) {
		t.Fatalf("got %+v %v", e, err)
	}
	if ufe.Field != "enable_att" || ufe.Reason != VerdictIgnored || ufe.Employee == nil || ufe.Employee.ID != 7 {
		t.Errorf("%+v", ufe)
	}
	if !strings.Contains(err.Error(), "enable_att") || !strings.Contains(err.Error(), "employee 7") {
		t.Error(err)
	}
	if gets.Load() != 0 {
		t.Error("detail fetched although the write response carried the flag")
	}
	if e, err := c.Employees.Create(ctx, params); e != nil || !errors.As(err, &ufe) || ufe.Employee.ID != 7 {
		t.Errorf("create: %+v %v", e, err)
	}

	// 8.x echoes the flag in the write response.
	mode.Store(echoed)
	if e, err := c.Employees.Update(ctx, 7, params); err != nil || e.ID != 7 {
		t.Errorf("echoed flag: %+v %v", e, err)
	}

	// 9.0 omits the settings from the write response; the detail view decides.
	mode.Store(silentOK)
	gets.Store(0)
	e, err = c.Employees.Create(ctx, params)
	if err != nil || e == nil || e.ID != 7 || gets.Load() != 1 {
		t.Errorf("silent but applied: %+v %v (gets %d)", e, err, gets.Load())
	}
	mode.Store(silentWrong)
	if _, err := c.Employees.Create(ctx, params); !errors.As(err, &ufe) || ufe.Reason != VerdictIgnored {
		t.Errorf("silent and ignored: %v", err)
	}
	mode.Store(neverShown)
	if _, err := c.Employees.Update(ctx, 7, params); !errors.As(err, &ufe) || ufe.Reason != VerdictNotReported {
		t.Errorf("never reported: %v", err)
	}

	// The read-back fails: no verdict, the record and the cause are both
	// reachable, and the sentinel does not match.
	mode.Store(readBackFails)
	_, err = c.Employees.Create(ctx, params)
	if !errors.As(err, &ufe) || ufe.Reason != VerdictUnverified || ufe.Employee == nil || ufe.Employee.ID != 7 || ufe.Field != "" {
		t.Fatalf("read-back failure: %v", err)
	}
	if errors.Is(err, ErrUnsupportedField) || !errors.Is(err, ErrUnverified) {
		t.Error("an unverified write must match ErrUnverified, not ErrUnsupportedField")
	}
	if !strings.HasPrefix(err.Error(), ErrUnverified.Error()) {
		t.Errorf("message names the wrong state: %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("cause not reachable: %v", err)
	}
	if !strings.Contains(err.Error(), "unverified") || !strings.Contains(err.Error(), "employee 7") || !strings.Contains(err.Error(), "boom") {
		t.Error(err)
	}

	// The write response carries no id: nothing to read back.
	mode.Store(noID)
	gets.Store(0)
	if _, err := c.Employees.Create(ctx, params); !errors.As(err, &ufe) || ufe.Reason != VerdictNoID || gets.Load() != 0 || !errors.Is(err, ErrUnverified) || errors.Is(err, ErrUnsupportedField) {
		t.Errorf("no id: %v (gets %d)", err, gets.Load())
	}

	// One requested flag echoed, another not: the detail view is consulted
	// for the missing one instead of reporting it as unsupported.
	mode.Store(partialEcho)
	gets.Store(0)
	if e, err := c.Employees.Update(ctx, 7, &EmployeeParams{EnableAtt: new(false), EnableOvertime: new(true)}); err != nil || e == nil || gets.Load() != 1 {
		t.Errorf("partial echo: %+v %v (gets %d)", e, err, gets.Load())
	}

	// Flags that were not requested are neither checked nor fetched.
	mode.Store(neverShown)
	gets.Store(0)
	if _, err := c.Employees.Update(ctx, 7, &EmployeeParams{CardNo: new("1")}); err != nil || gets.Load() != 0 {
		t.Errorf("unrelated update: %v (gets %d)", err, gets.Load())
	}

	// The error type is safe to format without a record.
	if s := (&UnsupportedFieldError{Reason: VerdictIgnored}).Error(); !strings.Contains(s, "employee unknown") || !strings.HasPrefix(s, ErrUnsupportedField.Error()) {
		t.Errorf("nil-record Error(): %q", s)
	}
}

func TestHeaderOptionsRejectControlCharacters(t *testing.T) {
	for _, opt := range []Option{WithLanguage("en\r\nX: 1"), WithUserAgent("bad\nagent")} {
		if _, err := New("http://x", opt); err == nil {
			t.Error("control characters accepted in a header option")
		}
	}
	if _, err := New("http://x", WithLanguage("ru-RU, ru;q=0.9"), WithUserAgent("app/1.0 (tab\tok)")); err != nil {
		t.Errorf("valid header values rejected: %v", err)
	}
}

func TestGetByCode(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// The server matches emp_code as a prefix; the client must pick the
		// exact one, even when it is not on the first page.
		if r.URL.Query().Get("emp_code") != "1" {
			f.page(w, 0, "")
			return
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			f.page(w, 3, srv.URL+"/personnel/api/employees/?emp_code=1&page=2", map[string]any{"id": 10, "emp_code": "10"}, map[string]any{"id": 11, "emp_code": "11"})
		default:
			f.page(w, 3, "", map[string]any{"id": 1, "emp_code": "1"})
		}
	}
	c := newTestClient(t, srv, WithVersion(Version8))
	e, err := c.Employees.GetByCode(t.Context(), "1")
	if err != nil || e.ID != 1 {
		t.Fatal(e, err)
	}
	_, err = c.Employees.GetByCode(t.Context(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestTransactionsFilterAndDecoding(t *testing.T) {
	SetLocation(time.FixedZone("srv", 3*3600))
	t.Cleanup(func() { SetLocation(nil) })

	f, srv := newFakeServer(t, Version8, AuthJWT)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":2,"next":null,"previous":null,"results":[
			{"id":1,"emp":17,"emp_code":"100001","first_name":"Harry","punch_time":"2019-03-04 09:50:00","punch_state":"0",
			 "punch_state_display":"Check In","verify_type":1,"verify_type_display":"Fingerprint","terminal_sn":"CKJF201260077",
			 "temperature":"36.4","is_mask":1,"upload_time":"2019-03-04 09:50:01"},
			{"id":2,"emp":null,"emp_code":"100002","punch_time":"2019-03-04 18:00:00","punch_state":5,"verify_type":"15",
			 "temperature":null,"is_mask":null,"terminal":{"id":3,"sn":"X"},"custom":true}
		]}`)
	}
	c := newTestClient(t, srv, WithVersion(Version8), WithAuthScheme(AuthJWT))

	start := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	page, err := c.Transactions.List(t.Context(), &TransactionFilter{
		EmpCode: "1", TerminalSN: "SN", TerminalAlias: "Gate", StartTime: start, EndTime: start.Add(24 * time.Hour),
		ListOptions: ListOptions{Ordering: "punch_time"},
	})
	if err != nil {
		t.Fatal(err)
	}
	q := f.lastQuery()
	if q.Get("start_time") != "2019-03-01 03:00:00" || q.Get("end_time") != "2019-03-02 03:00:00" {
		t.Errorf("time filters %v", q)
	}
	if q.Get("emp_code") != "1" || q.Get("terminal_sn") != "SN" || q.Get("terminal_alias") != "Gate" || q.Get("ordering") != "punch_time" {
		t.Errorf("query %v", q)
	}

	if len(page.Results) != 2 {
		t.Fatalf("%+v", page)
	}
	a, b := page.Results[0], page.Results[1]
	if a.Emp.ID != 17 || a.PunchState != PunchCheckIn || a.PunchStateDisplay != "Check In" || a.VerifyType != 1 {
		t.Errorf("a: %+v", a)
	}
	if a.Temperature == nil || *a.Temperature != 36.4 || a.IsMask == nil || *a.IsMask != 1 {
		t.Errorf("a temp/mask: %+v", a)
	}
	if a.PunchTime.Hour() != 9 || a.PunchTime.Location().String() != "srv" {
		t.Errorf("a time: %v", a.PunchTime)
	}
	if !b.Emp.IsZero() || b.PunchState != PunchOvertimeOut || b.VerifyType != 15 || b.Temperature != nil || b.IsMask != nil {
		t.Errorf("b: %+v", b)
	}
	if b.Terminal.ID != 3 || b.Terminal.Object == nil || b.Terminal.Object.SN != "X" {
		t.Errorf("b terminal: %+v", b.Terminal)
	}
	if string(b.Extra["custom"]) != "true" {
		t.Errorf("b extra: %v", b.Extra)
	}
}

func TestDoEscapeHatch(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/att/api/manualLogs/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}
	c, err := New(srv.URL+"/prefix", WithCredentials("admin", "secret"), WithUserAgent("test-agent"))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	// Missing trailing slash is added; raw body is passed through.
	err = c.Do(t.Context(), http.MethodPost, "att/api/manualLogs", url.Values{"x": {"1"}}, json.RawMessage(`{"a":1}`), &out)
	if err != nil || !out.OK {
		t.Fatal(out, err)
	}
	if string(f.lastBody()) != `{"a":1}` || f.lastQuery().Get("x") != "1" || f.lastRequest().Header.Get("User-Agent") != "test-agent" {
		t.Errorf("%s %v %s", f.lastBody(), f.lastQuery(), f.lastRequest().Header.Get("User-Agent"))
	}

	// Reader bodies are supported too; a nil out discards the response.
	if err := c.Do(t.Context(), http.MethodPost, "/att/api/manualLogs/", nil, strings.NewReader(`{"b":2}`), nil); err != nil {
		t.Fatal(err)
	}
	if string(f.lastBody()) != `{"b":2}` {
		t.Error(string(f.lastBody()))
	}
}

func TestContextCancellation(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}
	c := newTestClient(t, srv, WithToken("tok-1"))
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Areas.Get(ctx, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v", err)
	}
}

func TestErrorParsing(t *testing.T) {
	e := newError("GET", "http://x/", 500, []byte("<html>Server Error</html>"))
	if e.Message != "" || e.Error() != "biotime: GET http://x/: 500 Internal Server Error" {
		t.Error(e.Error())
	}
	e = newError("GET", "http://x/", 502, []byte("bad gateway"))
	if e.Message != "bad gateway" {
		t.Error(e.Message)
	}
	e = newError("POST", "http://x/", 400, []byte(`{"code":5,"msg":"nope","department":{"id":["Invalid pk"]}}`))
	if e.Code != 5 || e.Message != "nope" || e.Fields["department"][0] != "id: Invalid pk" {
		t.Errorf("%+v", e)
	}
	if !errors.Is(e, ErrValidation) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrUnauthorized) {
		t.Error("sentinel matching")
	}
	if !errors.Is(newError("GET", "u", 403, nil), ErrUnauthorized) {
		t.Error("403 should match ErrUnauthorized")
	}
}

func TestRedirectIsAnError(t *testing.T) {
	// An http->https proxy or a PREPEND_WWW rule answers 301. Following it
	// would turn the POST into a GET of the list endpoint and decode a page
	// envelope into an Employee without complaint.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("moved") == "" {
			http.Redirect(w, r, srv.URL+r.URL.Path+"?moved=1", http.StatusMovedPermanently)
			return
		}
		fmt.Fprint(w, `{"count":1,"next":null,"previous":null,"code":0,"msg":"","data":[{"id":7}]}`)
	}))
	t.Cleanup(srv.Close)

	// The default client and a caller-supplied one configured as the
	// WithHTTPClient documentation prescribes must behave the same.
	custom := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for name, opts := range map[string][]Option{
		"default client": {WithToken("tok")},
		"custom client":  {WithToken("tok"), WithHTTPClient(custom)},
	} {
		c, _ := New(srv.URL, opts...)
		emp, err := c.Employees.Create(t.Context(), &EmployeeParams{EmpCode: new("x")})
		if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.StatusCode != http.StatusMovedPermanently || emp != nil {
			t.Fatalf("%s: got %+v, %v", name, emp, err)
		}
	}
}

func TestIterationFollowsNextLink(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	// An offset-paginating server that ignores "page" and, being behind a
	// proxy, advertises an internal address in "next".
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		switch offset {
		case 0:
			f.page(w, 3, "http://10.0.0.5:8080/iclock/api/terminals/?limit=2&offset=2", map[string]any{"id": 1}, map[string]any{"id": 2})
		case 2:
			f.page(w, 3, "", map[string]any{"id": 3})
		default:
			http.NotFound(w, r)
		}
	}
	c := newTestClient(t, srv)
	all, err := Collect(c.Terminals.All(t.Context(), &TerminalFilter{SN: "keep", ListOptions: ListOptions{PageSize: 2}}))
	if err != nil || len(all) != 3 || all[2].ID != 3 {
		t.Fatalf("%v %v", all, err)
	}
	if q := f.lastQuery(); q.Get("offset") != "2" || q.Get("sn") != "keep" || q.Get("limit") != "2" {
		t.Errorf("second page query %v", q)
	}
	if host := f.lastRequest().Host; strings.Contains(host, "10.0.0.5") {
		t.Errorf("followed the advertised host %q", host)
	}
}

func TestIterationDetectsRepeatedPage(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	// A server that ignores paging entirely and always claims a next page:
	// without a guard this is an infinite loop of page 1.
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		f.page(w, 99, srv.URL+"/personnel/api/areas/?page=2", map[string]any{"id": 1})
	}
	c := newTestClient(t, srv)
	all, err := Collect(c.Areas.All(t.Context(), nil))
	if err == nil || !strings.Contains(err.Error(), "repeated page") {
		t.Fatalf("expected repeated page error, got %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected the two pages served before detection, got %d", len(all))
	}
}

func TestBodySizeLimit(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"count":0,"next":null,"previous":null,"code":0,"msg":"`+strings.Repeat("x", 100)+`","data":[]}`)
	}
	c := newTestClient(t, srv, WithMaxBodySize(64))
	_, err := c.Areas.List(t.Context(), nil)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("response over the cap: got %v", err)
	}
	// Reader request bodies are buffered for the 401 replay and share the cap.
	err = c.Do(t.Context(), http.MethodPost, "/x/", nil, strings.NewReader(strings.Repeat("y", 65)), nil)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("request over the cap: got %v", err)
	}

	// "No limit" must not overflow into "read nothing".
	for _, n := range []int64{1024, math.MaxInt64} {
		c = newTestClient(t, srv, WithMaxBodySize(n))
		page, err := c.Areas.List(t.Context(), nil)
		if err != nil || page.Count != 0 || page.Msg == "" {
			t.Fatalf("limit %d: %+v %v", n, page, err)
		}
	}
}

func TestIteratorIsRestartable(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			f.page(w, 2, srv.URL+"/personnel/api/areas/?page=2", map[string]any{"id": 1})
		default:
			f.page(w, 2, "", map[string]any{"id": 2})
		}
	}
	c := newTestClient(t, srv)
	seq := c.Areas.All(t.Context(), nil)
	for run := range 2 {
		got, err := Collect(seq)
		if err != nil || len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
			t.Fatalf("run %d: %v %v", run, got, err)
		}
	}
}
