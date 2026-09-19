package biotime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout   = 30 * time.Second
	defaultUserAgent = "zkteco-biotime-go-client"
	defaultLanguage  = "en"
	defaultMaxBody   = 32 << 20
	defaultType      = "application/json"
	// loginBackoff is how long an automatic re-login stays suspended after
	// the server rejected the credentials. Without it, every request in
	// flight when a token expires would retry the login with the same bad
	// password, which is how an integration account gets locked out during
	// a password rotation. [Client.Login] is never throttled.
	loginBackoff = time.Minute
)

// Client talks to a ZKBio Time server. Create one with [New]. A Client is
// safe for concurrent use.
type Client struct {
	baseURL       *url.URL
	http          *http.Client
	timeout       time.Duration
	maxBody       int64
	version       Version
	pageSizeParam string
	scheme        AuthScheme
	userAgent     string
	language      string
	logger        *slog.Logger

	creds *credentials
	// now is the clock behind loginBackoff; tests replace it.
	now func() time.Time

	// tokenMu guards token and the last rejected login.
	tokenMu     sync.RWMutex
	token       string
	loginErr    error     // the rejection, nil after a success or SetToken
	loginFailed time.Time // when loginErr was recorded
	// loginMu serializes automatic logins so that concurrent requests
	// share one.
	loginMu sync.Mutex

	// Employees manages personnel records (/personnel/api/employees/).
	Employees *EmployeeService
	// Departments manages the department tree (/personnel/api/departments/).
	Departments *DepartmentService
	// Areas manages device areas (/personnel/api/areas/).
	Areas *AreaService
	// Positions manages job positions (/personnel/api/positions/).
	Positions *PositionService
	// Terminals lists attendance devices (/iclock/api/terminals/).
	Terminals *TerminalService
	// Transactions lists attendance punches (/iclock/api/transactions/).
	Transactions *TransactionService
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// New returns a client for the server at baseURL, for example
// "http://biotime.example.com:8080". The path component of baseURL, if any,
// is used as a prefix for every request.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("biotime: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("biotime: base URL must use http or https, got %q", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("biotime: base URL has no host: %q", baseURL)
	}
	// Credentials in the base URL cannot work and cannot stay secret. There
	// is one Authorization header and the server's token scheme owns it, so
	// the transport would send basic auth on the login request and drop it
	// on every authenticated one; and url.URL renders userinfo in clear
	// text wherever the address is printed.
	if u.User != nil {
		return nil, errors.New("biotime: base URL must not carry credentials: " +
			"they would be sent only on the login request, because the server's " +
			"token scheme owns the Authorization header, and they would appear " +
			"in errors and logs; give a proxy's credentials to a custom " +
			"transport passed to WithHTTPClient instead")
	}

	c := &Client{
		baseURL:   u,
		timeout:   defaultTimeout,
		maxBody:   defaultMaxBody,
		version:   Version9,
		scheme:    AuthToken,
		userAgent: defaultUserAgent,
		language:  defaultLanguage,
		now:       time.Now,
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	// Derived settings are resolved after every option ran, so option order
	// does not matter.
	if c.pageSizeParam == "" {
		c.pageSizeParam = c.version.pageSizeParam()
	}
	if c.http == nil {
		c.http = &http.Client{
			Timeout: c.timeout,
			// A followed redirect turns POST into GET and drops the body, and
			// the response of the wrong endpoint would then be decoded without
			// complaint. Surface the 3xx instead; request maps it to an *Error.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	c.Employees = &EmployeeService{resource: newResource[Employee, EmployeeParams, *EmployeeFilter](c, employeesPath)}
	c.Departments = &DepartmentService{resource: newResource[Department, DepartmentParams, *DepartmentFilter](c, departmentsPath)}
	c.Areas = &AreaService{resource: newResource[Area, AreaParams, *AreaFilter](c, areasPath)}
	c.Positions = &PositionService{resource: newResource[Position, PositionParams, *PositionFilter](c, positionsPath)}
	c.Terminals = &TerminalService{collection: newCollection[Terminal, *TerminalFilter](c, terminalsPath)}
	c.Transactions = &TransactionService{collection: newCollection[Transaction, *TransactionFilter](c, transactionsPath)}
	return c, nil
}

// BaseURL returns the server address the client was created with.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// Version returns the server generation the client is configured for.
func (c *Client) Version() Version { return c.version }

// Token returns the access token currently in use, or "" before login.
func (c *Client) Token() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// SetToken replaces the access token in use and lifts the re-login backoff
// that a rejected login may have set.
func (c *Client) SetToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = token
	c.loginErr = nil
}

// recentLoginFailure returns an error wrapping the last rejected login when
// it happened less than loginBackoff ago, else nil. Each caller gets its
// own wrapper around the shared rejection, and the refusal is logged, so a
// trace shows which requests never reached the server.
func (c *Client) recentLoginFailure(ctx context.Context) error {
	// Snapshot and release before logging: the logger is the caller's code
	// and must never run under a lock of this package.
	c.tokenMu.RLock()
	loginErr, failedAt := c.loginErr, c.loginFailed
	c.tokenMu.RUnlock()
	if loginErr == nil {
		return nil
	}
	remaining := loginBackoff - c.now().Sub(failedAt)
	if remaining <= 0 {
		return nil
	}
	c.log(ctx, "biotime: login suspended after rejection", "scheme", string(c.scheme), "retry_in", remaining)
	return fmt.Errorf("biotime: login suspended for %s after a rejection: %w", loginBackoff, loginErr)
}

func (c *Client) recordLoginFailure(err error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.loginErr = err
	c.loginFailed = c.now()
}

// Login obtains a fresh access token with the configured credentials and
// stores it in the client. Calling it explicitly is optional: the first
// request that needs a token logs in automatically.
//
// A refused login (401, 403, or the 400 with field errors that Django REST
// framework uses for bad credentials) returns an error matching
// [ErrUnauthorized]; the 400 form also matches [ErrValidation]. Either can
// be unwrapped into an [*Error].
//
// When the refusal carries the server's own JSON verdict, the automatic
// login is suspended for one minute and every request in that window fails
// with an error wrapping the rejection, so that a fleet of workers hitting
// an expired token does not retry a bad password once per request and trip
// the server's lockout. A refusal without a JSON body, such as an edge
// device's block page, and any 5xx or transport failure suspend nothing.
// Login itself is never suspended, and [Client.SetToken] lifts the
// suspension.
func (c *Client) Login(ctx context.Context) (string, error) {
	if c.creds == nil {
		return "", ErrNoCredentials
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := c.request(ctx, http.MethodPost, c.scheme.loginPath(), nil, c.creds, &resp, false); err != nil {
		// Rejected credentials come back as 401, 403, or Django REST
		// framework's 400 with field errors ("non_field_errors"). Anything
		// else, a 502 from a proxy, a 500 from the server or a proxy's
		// bare 400, is not a verdict on the credentials: it is neither
		// "unauthorized" nor a reason to suspend re-login.
		if apiErr, ok := errors.AsType[*Error](err); ok && isLoginRejection(apiErr) {
			c.log(ctx, "biotime: login rejected", "scheme", string(c.scheme), "status", apiErr.StatusCode)
			apiErr.login = true
			// The request was refused, so the error is "unauthorized" either
			// way; but only a verdict the server itself wrote suspends the
			// re-login. Django REST framework always sends a JSON "detail"
			// or field errors, and newError discards HTML bodies, so a
			// message-less 403 is an edge device's block page, not a
			// rejected password.
			if apiErr.Message != "" || len(apiErr.Fields) > 0 {
				c.recordLoginFailure(err)
			}
		}
		return "", err
	}
	if resp.Token == "" {
		return "", errors.New("biotime: login response contained no token")
	}
	c.SetToken(resp.Token)
	c.log(ctx, "biotime: logged in", "scheme", string(c.scheme))
	return resp.Token, nil
}

// isLoginRejection reports whether a login failure is the server's verdict
// on the credentials rather than a transport or proxy problem.
func isLoginRejection(e *Error) bool {
	return e.Is(ErrUnauthorized) || (e.StatusCode == http.StatusBadRequest && len(e.Fields) > 0)
}

// ensureToken returns the current token, logging in first when none is set.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	if t := c.Token(); t != "" {
		return t, nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if t := c.Token(); t != "" {
		return t, nil
	}
	if err := c.recentLoginFailure(ctx); err != nil {
		return "", err
	}
	return c.Login(ctx)
}

// refreshToken obtains a new token after the server rejected the one used
// for a request. When another goroutine has already replaced the rejected
// token, the replacement is returned without logging in again.
func (c *Client) refreshToken(ctx context.Context, rejected string) (string, error) {
	if c.creds == nil {
		return "", nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if t := c.Token(); t != "" && t != rejected {
		return t, nil
	}
	if err := c.recentLoginFailure(ctx); err != nil {
		return "", err
	}
	c.log(ctx, "biotime: token rejected, re-authenticating", "scheme", string(c.scheme))
	return c.Login(ctx)
}

// TypedBody is a request body with an explicit Content-Type, for the
// endpoints [Client.Do] reaches that do not take JSON, such as a photo
// upload. Content follows the same rules as the body parameter of
// [Client.Do]: an [io.Reader], a []byte or a [json.RawMessage] is sent
// unchanged, anything else is encoded as JSON. An empty ContentType means
// "application/json".
//
//	f, err := os.Open("badge.png")
//	...
//	err = client.Do(ctx, http.MethodPost, path, nil,
//		biotime.TypedBody{ContentType: "image/png", Content: f}, nil)
type TypedBody struct {
	ContentType string
	Content     any
}

// Do performs an authenticated request against an arbitrary API path and
// decodes the JSON response into out (which may be nil). It is the escape
// hatch for endpoints this package does not model. path is relative to the
// base URL, for example "/att/api/manualLogs/". body, when non-nil, is
// encoded as JSON unless it is an [io.Reader], a []byte or a
// [json.RawMessage], which are sent unchanged. Every body is sent as
// "application/json" unless it is wrapped in a [TypedBody], which names the
// Content-Type.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return c.request(ctx, method, path, query, body, out, true)
}

// Get is shorthand for [Client.Do] with the GET method and no body.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.Do(ctx, http.MethodGet, path, query, nil, out)
}

// Post is shorthand for [Client.Do] with the POST method.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPost, path, nil, body, out)
}

// request executes one API call, retrying once with a fresh token when the
// server answers 401 and credentials are available.
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body, out any, auth bool) error {
	// Both forms are unwrapped: a *TypedBody that fell through would be
	// JSON-encoded as the wrapper struct and sent to the server as such.
	contentType := defaultType
	switch tb := body.(type) {
	case TypedBody:
		contentType, body = tb.ContentType, tb.Content
	case *TypedBody:
		if tb == nil {
			return errors.New("biotime: nil *TypedBody")
		}
		contentType, body = tb.ContentType, tb.Content
	}
	if contentType == "" {
		contentType = defaultType
	}
	if !validHeaderValue(contentType) {
		return fmt.Errorf("biotime: invalid Content-Type %q", contentType)
	}

	payload, err := encodeBody(body, c.maxBody)
	if err != nil {
		return err
	}
	target := c.baseURL.JoinPath(path)
	if !strings.HasSuffix(target.Path, "/") {
		// Django requires trailing slashes and otherwise redirects, which
		// would drop the body and the Authorization header.
		target.Path += "/"
	}
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	var token string
	if auth {
		token, err = c.ensureToken(ctx)
		if err != nil {
			return err
		}
	}

	status, respBody, err := c.send(ctx, method, target, payload, contentType, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && auth {
		fresh, err := c.refreshToken(ctx, token)
		if err != nil {
			return err
		}
		if fresh != "" {
			status, respBody, err = c.send(ctx, method, target, payload, contentType, fresh)
			if err != nil {
				return err
			}
		}
	}

	if status < 200 || status >= 300 {
		return newError(method, redactedURL(target), status, respBody)
	}
	if out == nil || len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("biotime: decoding %s %s response: %w", method, withoutQuery(target), err)
	}
	if env, ok := out.(envelope); ok {
		if code := env.envelopeCode(); code != 0 {
			e := newError(method, redactedURL(target), status, respBody)
			e.Code = code
			return e
		}
	}
	return nil
}

// envelope is implemented by response types that carry the 9.0 code/msg
// wrapper, so that application-level failures reported with HTTP 200 still
// surface as errors.
type envelope interface {
	envelopeCode() int
}

// send performs a single HTTP exchange and reads the whole body, up to the
// configured size limit.
func (c *Client) send(ctx context.Context, method string, target *url.URL, payload []byte, contentType, token string) (status int, body []byte, err error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, fmt.Errorf("biotime: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.language != "" {
		req.Header.Set("Accept-Language", c.language)
	}
	if payload != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", string(c.scheme)+" "+token)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// The transport wraps its own *url.Error around the complete URL,
		// so stripping only our prefix would still print the query, whose
		// values are filter terms such as names. A timeout is the most
		// frequently logged error there is; scrub the wrapped one too.
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			uerr.URL = withoutQuery(target)
		}
		return 0, nil, fmt.Errorf("biotime: %s %s: %w", method, withoutQuery(target), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = readCapped(resp.Body, c.maxBody)
	if err != nil {
		return 0, nil, fmt.Errorf("biotime: reading %s %s response: %w", method, withoutQuery(target), err)
	}
	// The query carries filter values such as names and employee codes;
	// the log line names the endpoint only.
	c.log(ctx, "biotime: request",
		"method", method,
		"url", withoutQuery(target),
		"status", resp.StatusCode,
		"bytes", len(body),
		"duration", time.Since(start),
	)
	return resp.StatusCode, body, nil
}

// withoutQuery renders u without its query, fragment and userinfo: filter
// values such as names, and any credentials that reached the address, do
// not belong in a message.
func withoutQuery(u *url.URL) string {
	bare := *u
	bare.User = nil
	bare.RawQuery = ""
	bare.Fragment = ""
	return bare.String()
}

// redactedURL renders u complete, query included, but without userinfo. It
// is what [Error.URL] carries. [New] refuses a base URL with credentials,
// so this is a second line of defense for an address that reaches an error
// by another route.
func redactedURL(u *url.URL) string {
	if u.User == nil {
		return u.String()
	}
	bare := *u
	bare.User = nil
	return bare.String()
}

// errBodyTooLarge is wrapped by readCapped when a body exceeds the limit.
var errBodyTooLarge = errors.New("body exceeds the configured size limit")

// readCapped reads r to the end, failing when it holds more than limit
// bytes. limit is below math.MaxInt64, so limit+1 cannot overflow.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (%d bytes)", errBodyTooLarge, limit)
	}
	return data, nil
}

// encodeBody turns body into the bytes to send. nil yields nil. Reader
// bodies are buffered so that a request rejected with 401 can be replayed
// after re-authentication, hence the cap.
func encodeBody(body any, maxBody int64) ([]byte, error) {
	switch b := body.(type) {
	case nil:
		return nil, nil
	case []byte:
		return b, nil
	case json.RawMessage:
		return b, nil
	case io.Reader:
		data, err := readCapped(b, maxBody)
		if err != nil {
			return nil, fmt.Errorf("biotime: reading request body: %w", err)
		}
		return data, nil
	default:
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("biotime: encoding request body: %w", err)
		}
		return data, nil
	}
}

func (c *Client) log(ctx context.Context, msg string, args ...any) {
	if c.logger == nil {
		return
	}
	c.logger.DebugContext(ctx, msg, args...)
}

// detailPath joins a collection path with an object identifier.
func detailPath(collection string, id int) string {
	return fmt.Sprintf("%s%d/", collection, id)
}
