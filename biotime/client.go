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
	defaultMaxBody   = 32 << 20
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
	logger        *slog.Logger

	creds *credentials

	tokenMu sync.RWMutex
	token   string
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
// "http://192.168.0.27:8080". The path component of baseURL, if any, is used
// as a prefix for every request.
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

	c := &Client{
		baseURL:   u,
		timeout:   defaultTimeout,
		maxBody:   defaultMaxBody,
		version:   Version9,
		scheme:    AuthToken,
		userAgent: defaultUserAgent,
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

// SetToken replaces the access token in use.
func (c *Client) SetToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = token
}

// Login obtains a fresh access token with the configured credentials and
// stores it in the client. Calling it explicitly is optional: the first
// request that needs a token logs in automatically.
//
// Rejected credentials are reported by the server as a 400 validation
// response; the returned error matches both [ErrUnauthorized] and
// [ErrValidation] and can be unwrapped into an [*Error].
func (c *Client) Login(ctx context.Context) (string, error) {
	if c.creds == nil {
		return "", ErrNoCredentials
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := c.request(ctx, http.MethodPost, c.scheme.loginPath(), nil, c.creds, &resp, false); err != nil {
		var apiErr *Error
		if errors.As(err, &apiErr) {
			c.log(ctx, "biotime: login rejected", "scheme", string(c.scheme), "status", apiErr.StatusCode)
			apiErr.login = true
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
	c.log(ctx, "biotime: token rejected, re-authenticating", "scheme", string(c.scheme))
	return c.Login(ctx)
}

// Do performs an authenticated request against an arbitrary API path and
// decodes the JSON response into out (which may be nil). It is the escape
// hatch for endpoints this package does not model. path is relative to the
// base URL, for example "/att/api/manualLogs/". body, when non-nil, is
// encoded as JSON unless it is an [io.Reader], a []byte or a
// [json.RawMessage], which are sent as is.
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

	status, respBody, err := c.send(ctx, method, target, payload, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && auth {
		fresh, err := c.refreshToken(ctx, token)
		if err != nil {
			return err
		}
		if fresh != "" {
			status, respBody, err = c.send(ctx, method, target, payload, fresh)
			if err != nil {
				return err
			}
		}
	}

	if status < 200 || status >= 300 {
		return newError(method, target.String(), status, respBody)
	}
	if out == nil || len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("biotime: decoding %s %s response: %w", method, target, err)
	}
	if env, ok := out.(envelope); ok {
		if code := env.envelopeCode(); code != 0 {
			e := newError(method, target.String(), status, respBody)
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
func (c *Client) send(ctx context.Context, method string, target *url.URL, payload []byte, token string) (status int, body []byte, err error) {
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
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", string(c.scheme)+" "+token)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("biotime: %s %s: %w", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = readCapped(resp.Body, c.maxBody)
	if err != nil {
		return 0, nil, fmt.Errorf("biotime: reading %s %s response: %w", method, target, err)
	}
	c.log(ctx, "biotime: request",
		"method", method,
		"url", target.String(),
		"status", resp.StatusCode,
		"bytes", len(body),
		"duration", time.Since(start),
	)
	return resp.StatusCode, body, nil
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
