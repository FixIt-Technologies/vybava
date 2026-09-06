package plaud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// cached is the on-disk shape of the access-token cache. Only the short-lived
// token lives here; the refresh token never does.
type cached struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// expirySlack refreshes a little early so a request never races expiry.
const expirySlack = 60 * time.Second

// ErrNoRefreshToken means the vault did not inject the long-lived token.
var ErrNoRefreshToken = errors.New(RefreshTokenEnv + " is not set — run this command through mcp__onyx__run_command with env_refs {" +
	RefreshTokenEnv + ": onyx://Plaud/Plaud%20OAuth%20refresh%20token/token}; no token stored yet? `plaud login --json` first")

// Session resolves an access token for data calls: the cache when it is
// still valid, otherwise a refresh with RefreshToken. Rotated is invoked when
// the refresh answer carried a new refresh token, so the caller can shout
// that the vault must be updated.
type Session struct {
	Config       Config
	RefreshToken string
	Rotated      func(Token)
}

// AccessToken returns a usable bearer token, refreshing and caching as needed.
func (s Session) AccessToken(ctx context.Context) (string, error) {
	cfg := s.Config.withDefaults()
	if token, ok := readCache(cfg); ok {
		return token, nil
	}
	if s.RefreshToken == "" {
		return "", ErrNoRefreshToken
	}
	token, err := cfg.Refresh(ctx, s.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := CacheAccessToken(cfg, token); err != nil {
		return "", err
	}
	if token.RefreshToken != "" && token.RefreshToken != s.RefreshToken && s.Rotated != nil {
		s.Rotated(token)
	}
	return token.AccessToken, nil
}

func readCache(cfg Config) (string, bool) {
	if cfg.CacheFile == "" {
		return "", false
	}
	data, err := os.ReadFile(cfg.CacheFile)
	if err != nil {
		return "", false
	}
	var c cached
	if json.Unmarshal(data, &c) != nil || c.AccessToken == "" {
		return "", false
	}
	if !c.ExpiresAt.IsZero() && cfg.Now().After(c.ExpiresAt.Add(-expirySlack)) {
		return "", false
	}
	return c.AccessToken, true
}

// CacheAccessToken stores the access token with 0600 permissions; a missing
// CacheFile disables caching.
func CacheAccessToken(cfg Config, token Token) error {
	if cfg.CacheFile == "" {
		return nil
	}
	c := cached{AccessToken: token.AccessToken}
	if token.ExpiresIn > 0 {
		c.ExpiresAt = cfg.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.CacheFile), 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	tmp := cfg.CacheFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write access-token cache: %w", err)
	}
	if err := os.Rename(tmp, cfg.CacheFile); err != nil {
		return fmt.Errorf("write access-token cache: %w", err)
	}
	return nil
}

// ClearCache drops the cached access token (used after a 401 from the API so
// the next call refreshes).
func ClearCache(cfg Config) error {
	if cfg.CacheFile == "" {
		return nil
	}
	if err := os.Remove(cfg.CacheFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
