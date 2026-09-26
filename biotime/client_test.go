package biotime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	for _, bad := range []string{
		"", "biotime.example.com:8080", "ftp://x", "http://",
		// Credentials in the address are refused: one Authorization header
		// cannot carry both basic auth and the server's token, and userinfo
		// is rendered in clear text wherever the address is printed.
		"http://admin:hunter2@host/", "https://admin@host/", "http://@host/",
	} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) succeeded", bad)
		}
	}
	if _, err := New("http://admin:hunter2@host/"); err == nil || !strings.Contains(err.Error(), "must not carry credentials") {
		t.Errorf("got %v", err)
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
			func() (int, error) { return idOf(c.Employees.Get(ctx, 5)) },
		},
		{
			"departments",
			func() (int, error) { p, err := c.Departments.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Departments.All(ctx, nil))) },
			func() (int, error) { return idOf(c.Departments.Get(ctx, 5)) },
		},
		{
			"areas",
			func() (int, error) { p, err := c.Areas.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Areas.All(ctx, nil))) },
			func() (int, error) { return idOf(c.Areas.Get(ctx, 5)) },
		},
		{
			"positions",
			func() (int, error) { p, err := c.Positions.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Positions.All(ctx, nil))) },
			func() (int, error) { return idOf(c.Positions.Get(ctx, 5)) },
		},
		{
			"terminals",
			func() (int, error) { p, err := c.Terminals.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Terminals.All(ctx, nil))) },
			func() (int, error) { return idOf(c.Terminals.Get(ctx, 5)) },
		},
		{
			"transactions",
			func() (int, error) { p, err := c.Transactions.List(ctx, nil); return pageID(p, err) },
			func() (int, error) { return firstID(Collect(c.Transactions.All(ctx, nil))) },
			func() (int, error) { return idOf(c.Transactions.Get(ctx, 5)) },
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

// firstID returns the id of the only object of a slice, read through its
// JSON encoding so that one helper serves every record type.
func firstID[T any](items []T, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	if len(items) != 1 {
		return 0, fmt.Errorf("expected one object, got %d", len(items))
	}
	b, err := json.Marshal(items[0])
	if err != nil {
		return 0, fmt.Errorf("encoding %T: %w", items[0], err)
	}
	var head struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return 0, fmt.Errorf("decoding id of %T: %w", items[0], err)
	}
	return head.ID, nil
}

// idOf returns the id of a single object, by the same route as firstID.
func idOf[T any](v *T, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	return firstID([]T{*v}, nil)
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
	// The token from that login was never accepted and is now rejected
	// again. One such rejection is tolerated as replication lag: one more
	// login, one more retry. A second fresh token rejected in a row is the
	// verdict: no re-login, the token is dropped, the re-login suspended,
	// and the request after that fails without reaching the server.
	before = len(f.requests)
	_, err = c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "just issued") {
		t.Fatalf("first fresh rejection: got %v", err)
	}
	if n := len(f.requests) - before; n != 3 {
		t.Errorf("expected 3 requests (401, login, 401), got %d", n)
	}
	before = len(f.requests)
	_, err = c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "just issued") {
		t.Fatalf("second fresh rejection: got %v", err)
	}
	if n := len(f.requests) - before; n != 1 {
		t.Errorf("expected 1 request (401), got %d", n)
	}
	before = len(f.requests)
	_, err = c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests) - before; n != 0 {
		t.Errorf("expected no request while suspended, got %d", n)
	}
}

// TestFreshTokenRejectedSuspendsRelogin covers the server that issues a
// token and then refuses every request carrying it, which is what a wrong
// AuthScheme looks like. Without the suspension every call would cost a
// login, and a fleet of workers would turn a config typo into a password
// check per request on the server. The first rejection is tolerated (see
// TestTransientRejectionOfFreshTokenIsRetried); the second is the verdict.
func TestFreshTokenRejectedSuspendsRelogin(t *testing.T) {
	var logins, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "token-auth") {
			logins.Add(1)
			fmt.Fprint(w, `{"token":"t"}`)
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"detail":"Invalid token header."}`)
	}))
	t.Cleanup(srv.Close)
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c := newTestClient(t, srv, WithLogger(logger))
	now := time.Now()
	c.now = func() time.Time { return now }

	for i := range 5 {
		_, err := c.Employees.Get(t.Context(), 1)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("call %d: got %v", i, err)
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
			t.Errorf("call %d: the server's 401 is not reachable: %v", i, err)
		}
		if i > 0 && !strings.Contains(err.Error(), `WithAuthScheme("Token")`) {
			t.Errorf("call %d: the error does not name the likely cause: %v", i, err)
		}
	}
	// Call 1: login, 401, login, 401. Call 2: 401, suspension. Calls 3
	// to 5: refused without a request.
	if logins.Load() != 2 || calls.Load() != 3 {
		t.Errorf("5 calls cost %d logins and %d requests; want 2 and 3, then suspension", logins.Load(), calls.Load())
	}
	if c.Token() != "" {
		t.Error("a token the server refused is still in use")
	}
	if !strings.Contains(logged.String(), "level=WARN msg=\"biotime: fresh token rejected again") {
		t.Errorf("not logged as a warning:\n%s", logged.String())
	}

	// After the backoff one more login is tried; the server has not
	// accepted a token since, so the next rejection suspends at once.
	now = now.Add(loginBackoff)
	if _, err := c.Employees.Get(t.Context(), 1); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if logins.Load() != 3 || calls.Load() != 4 {
		t.Errorf("after the backoff: %d logins and %d requests", logins.Load(), calls.Load())
	}

	// A token that worked once and is then rejected is expiry, and is
	// still followed by exactly one re-login.
	f, fsrv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c = newTestClient(t, fsrv)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	f.reject.Store(1)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 {
		t.Errorf("logins %d, want a re-login after the accepted token was rejected", f.logins)
	}
}

// TestTransientRejectionOfFreshTokenIsRetried: the first request with a
// new token reaches a replica the token has not replicated to yet. That
// is one 401 on a fresh token, and it costs one more login, not a minute
// of refusals.
func TestTransientRejectionOfFreshTokenIsRetried(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)
	f.reject.Store(1)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 || len(f.requests) != 4 {
		t.Errorf("logins %d requests %d; want login, 401, login, 200", f.logins, len(f.requests))
	}
	// The accepted token reset the count: the next fresh rejection is the
	// first of a new sequence, not the second of the old one.
	f.mu.Lock()
	f.token = "rotated"
	f.mu.Unlock()
	f.reject.Store(2) // the rotated token's first request, then the re-login's first
	before := len(f.requests)
	_, err := c.Areas.List(t.Context(), nil)
	if !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "suspended") {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests) - before; n != 3 {
		t.Errorf("expected 3 requests (401, login, 401), got %d", n)
	}
	if c.Token() == "" {
		t.Error("one fresh rejection must not drop the token")
	}
}

// TestTimeoutAppliesToCustomClient: a custom http.Client without a Timeout
// and a context without a deadline must not wait forever.
func TestTimeoutAppliesToCustomClient(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}
	c := newTestClient(t, srv, WithToken("tok-1"), WithHTTPClient(&http.Client{}), WithTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := c.Areas.Get(context.Background(), 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the client's timeout did not apply")
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

// loginRequests counts the requests the fake received on its login path.
func (f *fakeServer) loginRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasSuffix(r.URL.Path, f.scheme.loginPath()) {
			n++
		}
	}
	return n
}

func TestRejectedLoginIsNotRetriedPerRequest(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	// The handler drops debug lines: a rejected login and the suspension
	// are the events an operator must see, and they go out as warnings.
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c := newTestClient(t, srv, WithCredentials("admin", "wrong"), WithLogger(logger))
	now := time.Now()
	c.now = func() time.Time { return now }

	// A fleet of workers with a bad password: one login attempt, not one
	// per worker, and each worker gets the rejection.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Positions.List(t.Context(), nil); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("got %v", err)
			}
		})
	}
	wg.Wait()
	if n := f.loginRequests(); n != 1 {
		t.Errorf("login attempts %d", n)
	}
	// The refusals are visible in the trace and the error says so while
	// still matching the rejection's sentinels.
	if out := logged.String(); !strings.Contains(out, "level=WARN msg=\"biotime: login rejected\"") || !strings.Contains(out, "level=WARN msg=\"biotime: login suspended") {
		t.Errorf("rejection and suspension are not logged as warnings:\n%s", out)
	}
	if _, err := c.Positions.List(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "suspended") || !errors.Is(err, ErrValidation) {
		t.Errorf("got %v", err)
	}

	// Explicit Login is never throttled.
	if _, err := c.Login(t.Context()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("got %v", err)
	}
	if n := f.loginRequests(); n != 2 {
		t.Errorf("login attempts %d", n)
	}

	// After the backoff the automatic login is tried again.
	now = now.Add(loginBackoff)
	_, _ = c.Positions.List(t.Context(), nil)
	if n := f.loginRequests(); n != 3 {
		t.Errorf("login attempts %d", n)
	}

	// A token set by the caller lifts the suspension.
	c.SetToken("tok-1")
	if _, err := c.Positions.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedReloginIsNotRetriedPerRequest(t *testing.T) {
	f, srv := newFakeServer(t, Version8, AuthJWT)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	// A valid pre-issued token and a password that has since been rotated.
	c := newTestClient(t, srv, WithAuthScheme(AuthJWT), WithToken("tok-1"), WithCredentials("admin", "rotated"))
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.token = "tok-expired"
	f.mu.Unlock()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Areas.List(t.Context(), nil); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("got %v", err)
			}
		})
	}
	wg.Wait()
	if n := f.loginRequests(); n != 1 {
		t.Errorf("login attempts %d", n)
	}
}

func TestTransientLoginFailureIsRetried(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	var fail atomic.Bool
	fail.Store(true)
	outer := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && strings.HasSuffix(r.URL.Path, f.scheme.loginPath()) {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		outer.ServeHTTP(w, r)
	})
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)
	if _, err := c.Areas.List(t.Context(), nil); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	// A 502 on login is not a rejection: the next request tries again.
	fail.Store(false)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeForbiddenOnLoginDoesNotSuspend(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	var block atomic.Bool
	block.Store(true)
	outer := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() && strings.HasSuffix(r.URL.Path, f.scheme.loginPath()) {
			// A WAF's block page: 403 with HTML, no verdict from the server.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<html><body>Access denied</body></html>")
			return
		}
		outer.ServeHTTP(w, r)
	})
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)
	// Refused is refused: the error is unauthorized. But it is not the
	// server's verdict on the password, so nothing is suspended.
	if _, err := c.Areas.List(t.Context(), nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	block.Store(false)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if n := f.loginRequests(); n != 1 {
		t.Errorf("login attempts seen by the server %d; the block page must not suspend login", n)
	}
}

func TestProxyBadRequestOnLoginIsNotARejection(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	var fail atomic.Bool
	fail.Store(true)
	outer := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && strings.HasSuffix(r.URL.Path, f.scheme.loginPath()) {
			// A reverse proxy's 400: HTML, no field errors, no verdict.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "<html><body>400 Bad Request</body></html>")
			return
		}
		outer.ServeHTTP(w, r)
	})
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	c := newTestClient(t, srv)
	_, err := c.Areas.List(t.Context(), nil)
	if err == nil || errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrValidation) {
		t.Fatalf("got %v", err)
	}
	fail.Store(false)
	if _, err := c.Areas.List(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	// The proxy answered the first attempt before the fake saw it; the
	// retry is the one the fake recorded. A suspended login would have
	// recorded none.
	if n := f.loginRequests(); n != 1 {
		t.Errorf("login attempts seen by the server %d; the proxy error must not suspend login", n)
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
		PageSize: 2, Ordering: "-id", Search: "harry",
		Department: 3,
		AppStatus:  new(0),
		EmpCode:    "7",
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
	for e, err := range c.Employees.All(t.Context(), &EmployeeFilter{PageSize: 2}) {
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
	all, err := Collect(c.Employees.All(t.Context(), &EmployeeFilter{Page: 3}))
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

	page, err := c.Terminals.List(t.Context(), &TerminalFilter{PageSize: 2, SN: "A", Area: 9, IPAddress: "10.0.0.1", State: new(1)})
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

	_, err = c.Terminals.List(t.Context(), &TerminalFilter{Page: 9})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Code != 2 || apiErr.Message != "page out of range" || apiErr.StatusCode != http.StatusOK {
		t.Fatalf("got %v", err)
	}

	// The iterator surfaces the error and stops.
	var n int
	var iterErr error
	for _, err := range c.Terminals.All(t.Context(), &TerminalFilter{Page: 9}) {
		n++
		iterErr = err
	}
	if n != 1 || iterErr == nil {
		t.Errorf("n=%d err=%v", n, iterErr)
	}
}

func TestNonListResponseFailsTheWalk(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// A proxy, or a misrouted path, answering 200 with something that
		// is not a list. Yielding nothing here would look like an empty
		// resource.
		fmt.Fprint(w, `{"detail":"service index","version":"9.0"}`)
	}
	c := newTestClient(t, srv)
	all, err := Collect(c.Transactions.All(t.Context(), nil))
	if err == nil || !strings.Contains(err.Error(), "not a list") || len(all) != 0 {
		t.Fatalf("got %d rows, %v", len(all), err)
	}

	// A failure envelope still reaches the caller as an *Error carrying the
	// server's code and message, not as a decoding complaint.
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":3,"msg":"boom"}`)
	}
	_, err = c.Transactions.List(t.Context(), nil)
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Code != 3 || apiErr.Message != "boom" {
		t.Fatalf("got %v", err)
	}
}

func TestIterationFailsOnEmptyPageWithNextLink(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// A server that hands out no rows but claims there is a next page:
		// an overloaded view answering 200 with an empty list. Stopping
		// quietly would look like a complete export.
		f.page(w, 500, "next")
	}
	c := newTestClient(t, srv)
	all, err := Collect(c.Departments.All(t.Context(), &DepartmentFilter{Search: "Ivanova"}))
	if err == nil || !strings.Contains(err.Error(), "empty page") || len(all) != 0 {
		t.Fatal(all, err)
	}
	if strings.Contains(err.Error(), "Ivanova") {
		t.Errorf("error carries the filter: %v", err)
	}
	if n := len(f.requests); n != 2 {
		t.Errorf("expected login and one list request, got %d", n)
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

	// Value present and different: ignored, no extra request. The write
	// happened, so the record is returned with the verdict.
	e, err := c.Employees.Update(ctx, 7, params)
	var ufe *UnsupportedFieldError
	if e == nil || e.ID != 7 || !errors.Is(err, ErrUnsupportedField) || !errors.As(err, &ufe) {
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
	if e, err := c.Employees.Create(ctx, params); e == nil || e.ID != 7 || !errors.As(err, &ufe) || ufe.Employee != e {
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
	e, err = c.Employees.Create(ctx, params)
	if !errors.As(err, &ufe) || ufe.Reason != VerdictUnverified || e == nil || e.ID != 7 || ufe.Employee != e || ufe.Field != "" {
		t.Fatalf("read-back failure: %+v %v", e, err)
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

	// The write response carries no id: it is not a record, and it is
	// refused before any verdict rather than read back as employee 0.
	mode.Store(noID)
	gets.Store(0)
	if e, err := c.Employees.Create(ctx, params); e != nil || err == nil || !strings.Contains(err.Error(), "no id") || errors.As(err, &ufe) || gets.Load() != 0 {
		t.Errorf("no id: %+v %v (gets %d)", e, err, gets.Load())
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

	// An empty code is answered without a request: the filter builder omits
	// empty values, so asking would list the whole personnel table.
	before := len(f.requests)
	_, err = c.Employees.GetByCode(t.Context(), "")
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooManyCandidates) {
		t.Errorf("got %v", err)
	}
	if n := len(f.requests) - before; n != 0 {
		t.Errorf("an empty code sent %d requests", n)
	}
}

// TestNilParamsAreRefused covers every write entry point. A nil would
// otherwise be marshaled as the JSON literal null and sent; a server that
// answered 2xx to that left EmployeeService reading the params again after
// the write, which panicked with the record already changed.
func TestNilParamsAreRefused(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// A lenient endpoint: accepts anything, echoes a record.
		fmt.Fprint(w, `{"id":7,"emp_code":"7","enable_att":true}`)
	}
	c := newTestClient(t, srv)

	calls := map[string]func() error{
		"employees create":   func() error { _, err := c.Employees.Create(t.Context(), nil); return err },
		"employees update":   func() error { _, err := c.Employees.Update(t.Context(), 7, nil); return err },
		"departments create": func() error { _, err := c.Departments.Create(t.Context(), nil); return err },
		"departments update": func() error { _, err := c.Departments.Update(t.Context(), 7, nil); return err },
		"areas create":       func() error { _, err := c.Areas.Create(t.Context(), nil); return err },
		"areas update":       func() error { _, err := c.Areas.Update(t.Context(), 7, nil); return err },
		"positions create":   func() error { _, err := c.Positions.Create(t.Context(), nil); return err },
		"positions update":   func() error { _, err := c.Positions.Update(t.Context(), 7, nil); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, errNilParams) {
				t.Fatalf("got %v", err)
			}
		})
	}
	// Nothing reached the network, not even a login.
	if n := len(f.requests); n != 0 {
		t.Errorf("a nil params sent %d requests", n)
	}
}

func TestGetByCodeGivesUpOnTooManyCandidates(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// A prefix-matching server with a huge personnel table: every page
		// is full of near misses and links to the next one.
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		items := make([]any, 0, 100)
		for i := range 100 {
			items = append(items, map[string]any{"id": page*1000 + i, "emp_code": fmt.Sprintf("1%04d", page*100+i)})
		}
		f.page(w, 50000, fmt.Sprintf("%s/personnel/api/employees/?emp_code=1&page=%d", srv.URL, page+1), items...)
	}
	c := newTestClient(t, srv)
	_, err := c.Employees.GetByCode(t.Context(), "1")
	if !errors.Is(err, ErrTooManyCandidates) || errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests) - 1; n != maxCodeCandidates/100 {
		t.Errorf("expected %d list requests, got %d", maxCodeCandidates/100, n)
	}
}

func TestTransactionsFilterAndDecoding(t *testing.T) {
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
	c := newTestClient(t, srv, WithVersion(Version8), WithAuthScheme(AuthJWT), WithLocation(time.FixedZone("srv", 3*3600)))

	start := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	page, err := c.Transactions.List(t.Context(), &TransactionFilter{
		EmpCode: "1", TerminalSN: "SN", TerminalAlias: "Gate", StartTime: start, EndTime: start.Add(24 * time.Hour),
		Ordering: "punch_time",
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
	// A 400 without field errors is still a request that cannot succeed
	// on retry.
	if !errors.Is(newError("GET", "u", 400, []byte(`{"detail":"Malformed request."}`)), ErrValidation) {
		t.Error("bare 400 should match ErrValidation")
	}
	// Filter values are personal data; the message names the endpoint only,
	// the URL field keeps everything for callers who want it.
	e = newError("GET", "http://x/personnel/api/employees/?search=Ivanova&page=2", 404, nil)
	if s := e.Error(); strings.Contains(s, "Ivanova") || !strings.Contains(s, "http://x/personnel/api/employees/") {
		t.Error(s)
	}
	if !strings.Contains(e.URL, "search=Ivanova") {
		t.Error(e.URL)
	}
}

func TestTypedBody(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }
	c := newTestClient(t, srv)
	png := "\x89PNG\r\n\x1a\n binary"

	// A plain body keeps the package default.
	if err := c.Post(t.Context(), "/x/", map[string]any{"a": 1}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("default content type %q", got)
	}

	// Both the value and the pointer form are unwrapped: a *TypedBody that
	// fell through would be JSON-encoded as the wrapper struct.
	for name, body := range map[string]any{
		"value":   TypedBody{ContentType: "image/png", Content: strings.NewReader(png)},
		"pointer": &TypedBody{ContentType: "image/png", Content: []byte(png)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.Post(t.Context(), "/personnel/api/employees/42/photo/", body, nil); err != nil {
				t.Fatal(err)
			}
			if got := f.lastRequest().Header.Get("Content-Type"); got != "image/png" {
				t.Errorf("content type %q", got)
			}
			if got := string(f.lastBody()); got != png {
				t.Errorf("body %q, want the bytes unchanged", got)
			}
		})
	}

	// An empty ContentType means the default; Content still follows the
	// rules of the body parameter, so a struct is encoded as JSON.
	if err := c.Post(t.Context(), "/x/", TypedBody{Content: map[string]any{"a": 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("empty content type %q", got)
	}
	if got := string(f.lastBody()); got != `{"a":1}` {
		t.Errorf("body %q", got)
	}

	// The type is validated once here rather than by the transport on every
	// request, and a nil pointer is refused rather than dereferenced.
	if err := c.Post(t.Context(), "/x/", TypedBody{ContentType: "image/png\n", Content: []byte("x")}, nil); err == nil ||
		!strings.Contains(err.Error(), "invalid Content-Type") {
		t.Errorf("got %v", err)
	}
	if err := c.Post(t.Context(), "/x/", (*TypedBody)(nil), nil); err == nil || !strings.Contains(err.Error(), "nil *TypedBody") {
		t.Errorf("got %v", err)
	}

	// The type survives the re-authentication retry, which replays the body.
	f.reject.Store(1)
	if err := c.Post(t.Context(), "/x/", TypedBody{ContentType: "image/png", Content: []byte(png)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("content type after retry %q", got)
	}
	if got := string(f.lastBody()); got != png {
		t.Errorf("body after retry %q", got)
	}
}

func TestPrintedURLsDropUserinfo(t *testing.T) {
	// New refuses such an address, so these guard the paths that render one
	// should it ever arrive by another route.
	u, err := url.Parse("http://admin:hunter2@host:8080/personnel/api/employees/?search=Ivanova")
	if err != nil {
		t.Fatal(err)
	}
	if got := withoutQuery(u); got != "http://host:8080/personnel/api/employees/" {
		t.Errorf("withoutQuery = %q", got)
	}
	if got := redactedURL(u); got != "http://host:8080/personnel/api/employees/?search=Ivanova" {
		t.Errorf("redactedURL = %q", got)
	}
	plain, _ := url.Parse("http://host/x/?a=b")
	if got := redactedURL(plain); got != "http://host/x/?a=b" {
		t.Errorf("redactedURL without userinfo = %q", got)
	}
	// The value the transport puts in its own error is replaced with the
	// scrubbed one, so it must not be the weaker of the two.
	if strings.Contains(withoutQuery(u), "hunter2") || strings.Contains(redactedURL(u), "hunter2") {
		t.Error("password survived")
	}
}

func TestTransportErrorsOmitQuery(t *testing.T) {
	filter := &EmployeeFilter{Search: "Ivanova"}

	t.Run("timeout", func(t *testing.T) {
		f, srv := newFakeServer(t, Version9, AuthToken)
		f.handler = func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
		}
		c := newTestClient(t, srv, WithTimeout(50*time.Millisecond))
		_, err := c.Employees.List(t.Context(), filter)
		if err == nil {
			t.Fatal("expected a timeout")
		}
		// The transport's own *url.Error carries the URL a second time.
		if strings.Contains(err.Error(), "Ivanova") {
			t.Errorf("timeout error carries the filter: %v", err)
		}
		if !strings.Contains(err.Error(), "/personnel/api/employees/") {
			t.Errorf("timeout error names no endpoint: %v", err)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		f, srv := newFakeServer(t, Version9, AuthToken)
		f.handler = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not json") }
		c := newTestClient(t, srv)
		_, err := c.Employees.List(t.Context(), filter)
		if err == nil {
			t.Fatal("expected a decode error")
		}
		if strings.Contains(err.Error(), "Ivanova") {
			t.Errorf("decode error carries the filter: %v", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		f, srv := newFakeServer(t, Version9, AuthToken)
		f.handler = func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, strings.Repeat("x", 4096))
		}
		c := newTestClient(t, srv, WithMaxBodySize(64))
		_, err := c.Employees.List(t.Context(), filter)
		if err == nil || !errors.Is(err, errBodyTooLarge) {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), "Ivanova") {
			t.Errorf("read error carries the filter: %v", err)
		}
	})
}

func TestDebugLogOmitsQuery(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) { f.page(w, 0, "") }
	var logged strings.Builder
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c := newTestClient(t, srv, WithLogger(logger))
	if _, err := c.Employees.List(t.Context(), &EmployeeFilter{Search: "Ivanova"}); err != nil {
		t.Fatal(err)
	}
	out := logged.String()
	if strings.Contains(out, "Ivanova") || strings.Contains(out, "secret") || strings.Contains(out, "tok-1") {
		t.Errorf("log leaks filter values or credentials:\n%s", out)
	}
	if !strings.Contains(out, "/personnel/api/employees/") {
		t.Errorf("log names no endpoint:\n%s", out)
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
	all, err := Collect(c.Terminals.All(t.Context(), &TerminalFilter{SN: "keep", PageSize: 2}))
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
	all, err := Collect(c.Areas.All(t.Context(), &AreaFilter{Search: "Ivanova"}))
	if err == nil || !strings.Contains(err.Error(), "repeated page 2") || strings.Contains(err.Error(), "Ivanova") {
		t.Fatalf("expected a repeated page error naming the page and not the filter, got %v", err)
	}
	// The link is server-controlled; only a numeric page reaches the error.
	for link, want := range map[string]string{
		"/x/?page=7":                     "page 7",
		"/x/?page=1%0AERROR+forged+line": "a page with a non-numeric number",
		"/x/?offset=200":                 "the page at offset 200",
		"/x/?offset=%0A":                 "the next page",
		"/x/":                            "the next page",
		"://bad":                         "the next page",
	} {
		if got := pageRef(link); got != want {
			t.Errorf("pageRef(%q) = %q, want %q", link, got, want)
		}
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

// TestLocationIsPerClient: two servers in two zones from one process, and
// a change of zone on one client never reaches the other.
func TestLocationIsPerClient(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transactions/"):
			f.page(w, 1, "", json.RawMessage(`{"id":1,"emp_code":"1","punch_time":"2024-06-26 09:00:00",
				"emp":{"id":5,"emp_code":"1","hire_date":"2024-06-01","update_time":"2024-06-25 08:00:00"},
				"terminal":{"id":3,"sn":"X","last_activity":"2024-06-26 08:59:00"}}`))
		case strings.HasSuffix(r.URL.Path, "/transactions/1/"):
			fmt.Fprint(w, `{"id":1,"emp_code":"1","punch_time":"2024-06-26 09:00:00"}`)
		default:
			http.NotFound(w, r)
		}
	}
	east := time.FixedZone("east", 3*3600)
	west := time.FixedZone("west", -5*3600)
	moscow := newTestClient(t, srv, WithLocation(east))
	toronto := newTestClient(t, srv, WithLocation(west))

	since := time.Date(2024, 6, 26, 0, 0, 0, 0, time.UTC)
	var got [2]Transaction
	for i, c := range []*Client{moscow, toronto} {
		page, err := c.Transactions.List(t.Context(), &TransactionFilter{StartTime: since})
		if err != nil {
			t.Fatal(err)
		}
		got[i] = page.Results[0]
		want := since.In(c.Location()).Format(DateTimeLayout)
		if q := f.lastQuery().Get("start_time"); q != want {
			t.Errorf("%s: start_time %q want %q", c.Location(), q, want)
		}
	}
	if got[0].PunchTime.Location() != east || got[1].PunchTime.Location() != west {
		t.Errorf("zones: %v %v", got[0].PunchTime.Location(), got[1].PunchTime.Location())
	}
	// Same digits, eight hours apart as instants.
	if got[0].PunchTime.String() != got[1].PunchTime.String() || got[1].PunchTime.Sub(got[0].PunchTime.Time) != 8*time.Hour {
		t.Errorf("punch times: %v %v", got[0].PunchTime, got[1].PunchTime)
	}
	// Expanded objects are resolved too, dates included.
	tx := got[0]
	if tx.Emp.Object.UpdateTime.Location() != east || tx.Emp.Object.HireDate.Location() != east || tx.Terminal.Object.LastActivity.Location() != east {
		t.Errorf("nested: %v %v %v", tx.Emp.Object.UpdateTime.Location(), tx.Emp.Object.HireDate.Location(), tx.Terminal.Object.LastActivity.Location())
	}
	if !tx.Emp.Object.HireDate.Equal(time.Date(2024, 6, 1, 0, 0, 0, 0, east)) {
		t.Errorf("hire date: %v", tx.Emp.Object.HireDate)
	}

	// Get resolves like List does, and so does Do into a record type; into
	// anything else the wall clock keeps its UTC label.
	one, err := toronto.Transactions.Get(t.Context(), 1)
	if err != nil || one.PunchTime.Location() != west {
		t.Errorf("Get: %v %v", one, err)
	}
	var viaDo Transaction
	if err := toronto.Get(t.Context(), "/iclock/api/transactions/1/", nil, &viaDo); err != nil || viaDo.PunchTime.Location() != west {
		t.Errorf("Do: %v %v", viaDo.PunchTime.Location(), err)
	}
	var plain struct {
		PunchTime DateTime `json:"punch_time"`
	}
	if err := toronto.Get(t.Context(), "/iclock/api/transactions/1/", nil, &plain); err != nil || plain.PunchTime.Location() != time.UTC || plain.PunchTime.String() != "2024-06-26 09:00:00" {
		t.Errorf("Do into a plain struct: %v %v %v", plain.PunchTime.Location(), plain.PunchTime, err)
	}

	if _, err := New(srv.URL, WithLocation(nil)); err == nil {
		t.Error("nil location accepted")
	}
	if c, _ := New(srv.URL); c.Location() != time.Local {
		t.Errorf("default location %v", c.Location())
	}
}

// TestDetailResponseIsARecord: a single-object response is decoded with
// the same discipline as a page. A 9.0 envelope is unwrapped, a failure
// envelope with HTTP 200 is an error, and a body without an identifier is
// refused rather than returned as record 0.
func TestDetailResponseIsARecord(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	var body atomic.Value
	body.Store(`{"code":0,"msg":"success","data":{"id":42,"emp_code":"E1","update_time":"2024-06-26 09:00:00"}}`)
	f.handler = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body.Load().(string)) }
	c := newTestClient(t, srv, WithLocation(time.FixedZone("srv", 3*3600)))
	ctx := t.Context()

	// Enveloped detail, on every path that decodes one record.
	e, err := c.Employees.Get(ctx, 42)
	if err != nil || e.ID != 42 || e.EmpCode != "E1" || e.UpdateTime.Location().String() != "srv" {
		t.Fatalf("Get: %+v %v", e, err)
	}
	if _, present := e.Extra["code"]; present {
		t.Error("envelope members reported as custom attributes")
	}
	if e, err := c.Employees.Create(ctx, &EmployeeParams{EmpCode: new("E1")}); err != nil || e.ID != 42 {
		t.Errorf("Create: %+v %v", e, err)
	}
	if e, err := c.Employees.Update(ctx, 42, &EmployeeParams{CardNo: new("1")}); err != nil || e.ID != 42 {
		t.Errorf("Update: %+v %v", e, err)
	}

	// A failure envelope with HTTP 200 is an error carrying the code.
	body.Store(`{"code":1,"msg":"employee does not exist"}`)
	var apiErr *Error
	if _, err := c.Employees.Get(ctx, 42); !errors.As(err, &apiErr) || apiErr.Code != 1 || apiErr.Message != "employee does not exist" {
		t.Errorf("failure envelope: %v", err)
	}
	// A body without an id is not a record, whatever shape it has. On a
	// read that is a bad answer; on a write the server said 2xx, so the
	// error says the write may have happened and matches its sentinel.
	for _, b := range []string{`{"emp_code":"E1"}`, `{"detail":"maintenance"}`, `{"code":0,"msg":"ok","data":{"emp_code":"E1"}}`, `{"code":0,"msg":"ok","data":null}`} {
		body.Store(b)
		if e, err := c.Employees.Get(ctx, 42); e != nil || err == nil || !errors.Is(err, errNotARecord) || errors.Is(err, ErrWriteUnconfirmed) {
			t.Errorf("Get %s: %+v %v", b, e, err)
		}
		if e, err := c.Employees.Create(ctx, &EmployeeParams{EmpCode: new("E1")}); e != nil || !errors.Is(err, ErrWriteUnconfirmed) {
			t.Errorf("Create %s: %+v %v", b, e, err)
		}
		if e, err := c.Employees.Update(ctx, 42, &EmployeeParams{CardNo: new("1")}); e != nil || !errors.Is(err, ErrWriteUnconfirmed) || !strings.HasPrefix(err.Error(), ErrWriteUnconfirmed.Error()) {
			t.Errorf("Update %s: %+v %v", b, e, err)
		}
	}
	// A record with a custom attribute named "code" is a record: the
	// envelope is recognized by its shape, not by one member.
	body.Store(`{"id":5,"emp_code":"E5","code":"ABC"}`)
	if e, err := c.Employees.Get(ctx, 5); err != nil || e.ID != 5 || string(e.Extra["code"]) != `"ABC"` {
		t.Errorf("record with a code attribute: %+v %v", e, err)
	}
	body.Store(`{"id":6,"emp_code":"E6","code":7,"msg":"custom"}`)
	if e, err := c.Employees.Get(ctx, 6); err != nil || e.ID != 6 {
		t.Errorf("record with numeric code and msg attributes: %+v %v", e, err)
	}

	// Identifiers are positive; nothing is sent for anything else.
	before := len(f.requests)
	if _, err := c.Employees.Get(ctx, 0); err == nil {
		t.Error("Get(0) accepted")
	}
	if _, err := c.Employees.Update(ctx, -1, &EmployeeParams{}); err == nil {
		t.Error("Update(-1) accepted")
	}
	if err := c.Employees.Delete(ctx, 0); err == nil {
		t.Error("Delete(0) accepted")
	}
	if n := len(f.requests) - before; n != 0 {
		t.Errorf("%d requests sent for invalid ids", n)
	}
}

func TestDoRaw(t *testing.T) {
	f, srv := newFakeServer(t, Version9, AuthToken)
	png := "\x89PNG\r\n\x1a\n binary"
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/personnel/api/employees/42/photo/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", `attachment; filename="42.png"`)
		fmt.Fprint(w, png)
	}
	c := newTestClient(t, srv)

	// Authenticated like Do, undecoded, with status and headers.
	resp, err := c.DoRaw(t.Context(), http.MethodGet, "/personnel/api/employees/42/photo/", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(resp.Body) != png || resp.Header.Get("Content-Disposition") != `attachment; filename="42.png"` {
		t.Errorf("%+v", resp)
	}
	if f.logins != 1 || f.lastRequest().Header.Get("Authorization") != "Token tok-1" {
		t.Error("DoRaw did not authenticate")
	}
	// The re-authentication retry applies too.
	f.reject.Store(1)
	if _, err := c.DoRaw(t.Context(), http.MethodGet, "/personnel/api/employees/42/photo/", nil, nil); err != nil {
		t.Fatal(err)
	}
	// A non-2xx answer is an *Error, as with Do.
	var apiErr *Error
	if _, err := c.DoRaw(t.Context(), http.MethodGet, "/nope/", nil, nil); !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("got %v", err)
	}
}
