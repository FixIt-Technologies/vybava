// Package plaud talks to the Plaud developer API directly: OAuth 2.0
// authorization-code + PKCE for the public client, a short-lived access-token
// cache, and the handful of read-only data calls the retired @plaud-ai/mcp
// server wrapped (files, notes, transcript blocks, current user).
//
// The refresh token never touches disk here — it arrives in the
// PLAUD_REFRESH_TOKEN environment variable (injected from the onyx vault) and
// only the access token is cached, at CacheFile with 0600 permissions.
package plaud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultClientID     = "client_9c501dad-8a0d-40b2-a7b0-d1cb8787f674"
	DefaultAuthorizeURL = "https://web.plaud.ai/platform/oauth"
	DefaultTokenURL     = "https://platform.plaud.ai/developer/api/oauth/third-party/access-token"
	DefaultRefreshURL   = "https://platform.plaud.ai/developer/api/oauth/third-party/access-token/refresh"
	DefaultAPIBase      = "https://platform.plaud.ai/developer/api"
	// DefaultRedirectURI is the redirect the public client is registered
	// with (CALLBACK_PORT 8199 + CALLBACK_PATH /auth/callback in
	// @plaud-ai/mcp 0.3.8); Plaud rejects anything else.
	DefaultRedirectURI = "http://localhost:8199/auth/callback"
	// RefreshTokenEnv carries the long-lived token; the vault injects it.
	RefreshTokenEnv = "PLAUD_REFRESH_TOKEN"
	// ClientIDEnv overrides DefaultClientID.
	ClientIDEnv = "PLAUD_CLIENT_ID"
)

// Config is every endpoint and file the package touches; zero values fall
// back to the production defaults so tests can point at httptest servers.
type Config struct {
	ClientID     string
	AuthorizeURL string
	TokenURL     string
	RefreshURL   string
	APIBase      string
	RedirectURI  string
	// CacheFile holds the short-lived access token (default
	// ~/.plaud/access-token.json).
	CacheFile string
	// HTTP defaults to a client with a sane timeout.
	HTTP *http.Client
	// Now defaults to time.Now; tests freeze it.
	Now func() time.Time
}

// DefaultConfig resolves the production endpoints plus env overrides.
func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{CacheFile: filepath.Join(home, ".plaud", "access-token.json")}
	if id := os.Getenv(ClientIDEnv); id != "" {
		cfg.ClientID = id
	}
	return cfg.withDefaults(), nil
}

func (c Config) withDefaults() Config {
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	def(&c.ClientID, DefaultClientID)
	def(&c.AuthorizeURL, DefaultAuthorizeURL)
	def(&c.TokenURL, DefaultTokenURL)
	def(&c.RefreshURL, DefaultRefreshURL)
	def(&c.APIBase, DefaultAPIBase)
	def(&c.RedirectURI, DefaultRedirectURI)
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Token is a token endpoint response. Raw keeps every field the server sent
// (the capture flow reads `refresh_token` straight out of it); the typed
// fields are the ones this package acts on.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Raw          map[string]any
}

// MarshalJSON emits the server's response verbatim so stdout stays a faithful
// capture source.
func (t Token) MarshalJSON() ([]byte, error) { return json.Marshal(t.Raw) }

func parseToken(body []byte) (Token, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, fmt.Errorf("token response is not JSON: %w", err)
	}
	token := Token{Raw: raw}
	token.AccessToken, _ = raw["access_token"].(string)
	token.RefreshToken, _ = raw["refresh_token"].(string)
	if n, ok := raw["expires_in"].(float64); ok {
		token.ExpiresIn = int64(n)
	}
	if token.AccessToken == "" {
		return Token{}, errors.New("token response carries no access_token")
	}
	return token, nil
}

// PKCE is one authorization attempt's verifier/challenge/state triple.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCE mints a fresh S256 code verifier and CSRF state.
func NewPKCE() (PKCE, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return PKCE{}, err
	}
	state, err := randomURLSafe(16)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), State: state}, nil
}

func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ConsentURL is the browser URL that starts the consent flow.
func (c Config) ConsentURL(p PKCE) string {
	c = c.withDefaults()
	q := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {c.RedirectURI},
		"response_type":         {"code"},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.State},
	}
	return c.AuthorizeURL + "?" + q.Encode()
}

// Exchange trades an authorization code for tokens. The client is public:
// HTTP Basic carries "<client_id>:" with an empty secret.
func (c Config) Exchange(ctx context.Context, code string, p PKCE) (Token, error) {
	c = c.withDefaults()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.ClientID},
		"code":          {code},
		"redirect_uri":  {c.RedirectURI},
		"state":         {p.State},
		"code_verifier": {p.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.ClientID+":")))
	body, err := c.do(req, "token exchange")
	if err != nil {
		return Token{}, err
	}
	return parseToken(body)
}

// ErrRefreshRejected means Plaud no longer accepts the refresh token; the
// only cure is a new `plaud login`.
var ErrRefreshRejected = errors.New("refresh token rejected — run `plaud login --json` through the onyx capture flow and store the new token")

// Refresh trades the long-lived refresh token for a fresh access token. The
// response may rotate the refresh token; callers must surface that.
func (c Config) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	c = c.withDefaults()
	form := url.Values{"refresh_token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RefreshURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	body, err := c.do(req, "token refresh")
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) && status.Code == http.StatusUnauthorized {
			return Token{}, fmt.Errorf("%w (%s)", ErrRefreshRejected, status.Detail())
		}
		return Token{}, err
	}
	return parseToken(body)
}

// StatusError is a non-2xx answer with its body kept for diagnostics.
type StatusError struct {
	Op   string
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.Op, e.Code, e.Detail())
}

// Detail pulls Plaud's `detail` field out of the body when present.
func (e *StatusError) Detail() string {
	var parsed struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal([]byte(e.Body), &parsed) == nil && parsed.Detail != nil {
		if s, ok := parsed.Detail.(string); ok {
			return s
		}
		if b, err := json.Marshal(parsed.Detail); err == nil {
			return string(b)
		}
	}
	return strings.TrimSpace(e.Body)
}

// maxBodyBytes caps one response; a bigger one is an error, never a
// silently truncated "success". A var so the test can shrink it.
var maxBodyBytes int64 = 64 << 20

// ErrBodyTooLarge is returned when a response exceeds maxBodyBytes.
var ErrBodyTooLarge = errors.New("response body exceeds the size limit")

func (c Config) do(req *http.Request, op string) ([]byte, error) {
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", op, err)
	}
	if int64(len(body)) > maxBodyBytes {
		return nil, fmt.Errorf("%s: %w (%d MiB)", op, ErrBodyTooLarge, maxBodyBytes>>20)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &StatusError{Op: op, Code: res.StatusCode, Body: string(body)}
	}
	return body, nil
}
