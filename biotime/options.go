package biotime

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"
)

// Version identifies the generation of the ZKBio Time server the client talks to.
type Version int

const (
	// Version8 targets legacy BioTime 8.x servers (the API published under
	// /api/docs/ on the server). Pagination uses the "page_size" parameter.
	Version8 Version = 8
	// Version9 targets ZKBio Time 9.0 and newer servers. Pagination uses the
	// "limit" parameter and list responses carry a code/msg envelope.
	Version9 Version = 9
)

// String implements fmt.Stringer.
func (v Version) String() string {
	switch v {
	case Version8:
		return "8.x"
	case Version9:
		return "9.0"
	default:
		return "unknown"
	}
}

// pageSizeParam returns the query parameter that controls page size for the
// server generation.
func (v Version) pageSizeParam() string {
	if v == Version8 {
		return "page_size"
	}
	return "limit"
}

// AuthScheme selects how the client obtains and presents an access token.
type AuthScheme string

const (
	// AuthToken uses POST /api-token-auth/ and sends "Authorization: Token <t>".
	// This is the scheme documented for 9.0 and is also available on 8.x.
	AuthToken AuthScheme = "Token"
	// AuthJWT uses POST /jwt-api-token-auth/ and sends "Authorization: JWT <t>".
	// This is the scheme documented for the 8.x servers. JWT tokens expire, so
	// the client transparently re-authenticates when a request is rejected
	// with 401 and credentials are configured.
	AuthJWT AuthScheme = "JWT"
)

func (s AuthScheme) loginPath() string {
	if s == AuthJWT {
		return "/jwt-api-token-auth/"
	}
	return "/api-token-auth/"
}

// Option configures a [Client]. Options are independent of each other and
// may be passed in any order.
type Option func(*Client) error

// WithVersion sets the server generation. The default is [Version9].
func WithVersion(v Version) Option {
	return func(c *Client) error {
		if v != Version8 && v != Version9 {
			return errors.New("biotime: unsupported version")
		}
		c.version = v
		return nil
	}
}

// WithPageSizeParam overrides the query parameter used to request a page
// size, for servers that deviate from the documented default of their
// generation.
func WithPageSizeParam(name string) Option {
	return func(c *Client) error {
		if name == "" {
			return errors.New("biotime: empty page size parameter name")
		}
		c.pageSizeParam = name
		return nil
	}
}

// WithCredentials configures the username and password used to obtain an
// access token. The first authenticated request triggers a login, and a
// request rejected with 401 triggers exactly one re-login and retry.
func WithCredentials(username, password string) Option {
	return func(c *Client) error {
		if username == "" {
			return errors.New("biotime: empty username")
		}
		c.creds = &credentials{Username: username, Password: password}
		return nil
	}
}

// WithToken configures a pre-issued access token. It may be combined with
// [WithCredentials]; in that case the token is used until the server rejects
// it, after which the client logs in again.
func WithToken(token string) Option {
	return func(c *Client) error {
		c.token = token
		return nil
	}
}

// WithAuthScheme selects the login endpoint and Authorization header prefix.
// The default is [AuthToken].
func WithAuthScheme(s AuthScheme) Option {
	return func(c *Client) error {
		switch s {
		case AuthToken, AuthJWT:
			c.scheme = s
			return nil
		default:
			return errors.New("biotime: unsupported auth scheme")
		}
	}
}

// WithHTTPClient sets the underlying [http.Client]. Use it to configure TLS,
// proxies, or custom transports. The default client does not follow
// redirects, because a followed redirect turns a POST into a GET and
// silently decodes the wrong resource; a custom client should set
// CheckRedirect to return [http.ErrUseLastResponse] for the same reason.
// The timeout set with [WithTimeout] applies to a custom client as well,
// through the request context; a Timeout on the client itself is not
// needed.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) error {
		if hc == nil {
			return errors.New("biotime: nil http client")
		}
		c.http = hc
		return nil
	}
}

// WithTimeout bounds every single request, whatever HTTP client is in use;
// a shorter deadline on the caller's context wins. The default is 30
// seconds. A walk over many pages keeps its own long deadline while no one
// page can hang longer than this, and a custom client with no Timeout of
// its own cannot wait forever on a server that stopped answering.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) error {
		if d <= 0 {
			return errors.New("biotime: timeout must be positive")
		}
		c.timeout = d
		return nil
	}
}

// WithMaxBodySize caps the number of bytes buffered for a response body and
// for an [io.Reader] request body passed to [Client.Do]. Larger bodies fail
// with an error instead of being read into memory. The default is 32 MiB;
// pass [math.MaxInt64] for no practical limit.
func WithMaxBodySize(n int64) Option {
	return func(c *Client) error {
		if n <= 0 {
			return errors.New("biotime: max body size must be positive")
		}
		// readCapped reads limit+1 bytes to detect overflow; keep that sum
		// representable.
		c.maxBody = min(n, math.MaxInt64-1)
		return nil
	}
}

// validHeaderValue reports whether v can be sent as an HTTP header value:
// visible ASCII, space and tab, per RFC 9110. The transport rejects anything
// else on every request; checking here reports it once, at [New].
func validHeaderValue(v string) bool {
	for i := range len(v) {
		if c := v[i]; c != '\t' && (c < ' ' || c == 0x7f) {
			return false
		}
	}
	return true
}

// WithUserAgent sets the User-Agent header sent with every request.
func WithUserAgent(ua string) Option {
	return func(c *Client) error {
		if !validHeaderValue(ua) {
			return fmt.Errorf("biotime: invalid User-Agent %q", ua)
		}
		c.userAgent = ua
		return nil
	}
}

// WithLanguage sets the Accept-Language header sent with every request. The
// server localizes its error messages ("detail", field errors) to the
// negotiated language and falls back to the language configured in its
// settings, which need not be English. The default is "en", so that
// [Error.Message] and [Error.Fields] are predictable regardless of the
// server's locale; pass "" to send no header and get the server's default.
func WithLanguage(tag string) Option {
	return func(c *Client) error {
		if !validHeaderValue(tag) {
			return fmt.Errorf("biotime: invalid Accept-Language %q", tag)
		}
		c.language = tag
		return nil
	}
}

// WithLogger enables logging through the given [slog.Logger]: every
// request at [slog.LevelDebug], and a rejected or suspended login at
// [slog.LevelWarn], so that the one event an operator must see reaches a
// production log. Credentials and tokens are never logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) error {
		if l == nil {
			return errors.New("biotime: nil logger")
		}
		c.logger = l
		return nil
	}
}

// WithLocation sets the zone the server keeps its wall-clock times in. ZKBio
// Time stores and returns timestamps without zone information; the client
// resolves the ones it receives in this zone and formats the ones it sends,
// in filters and through [Client.DateTime] and [Client.Date], the same way.
// The default is [time.Local], which is wrong whenever the program runs in
// a different zone than the server, the norm in containers. Each client has
// its own zone, so one process can serve servers in several.
//
// A zone that observes daylight saving cannot express every timestamp the
// server may send: the hour a transition skips does not exist, and the hour
// it repeats is ambiguous. Neither is reported as an error; the skipped
// hour is normalized to a neighboring instant and the repeated one resolves
// to the earlier of the two. A server kept in UTC, or any zone without
// daylight saving, has neither problem.
func WithLocation(loc *time.Location) Option {
	return func(c *Client) error {
		if loc == nil {
			return errors.New("biotime: nil location")
		}
		c.loc = loc
		return nil
	}
}
