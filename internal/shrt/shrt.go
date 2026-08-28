package shrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// codeLen is the default code length; the store extends a code (up to
// maxCodeLen, the full hash) when its prefix collides with a different URL or
// a reserved path segment.
const (
	codeLen    = 7
	maxCodeLen = 52
)

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Code derives the permanent, idempotent short code for a URL.
func Code(long string) string {
	return CodeN(long, codeLen)
}

// CodeN is Code at an explicit prefix length (collision extension).
func CodeN(long string, n int) string {
	sum := sha256.Sum256([]byte(long))
	full := strings.ToLower(codeEncoding.EncodeToString(sum[:]))
	if n > len(full) {
		n = len(full)
	}
	return full[:n]
}

// Result is one shortened URL, JSON-stable for automation.
type Result struct {
	Long   string `json:"long"`
	Short  string `json:"short"`
	Static bool   `json:"static"`
	Minted bool   `json:"minted"`
}

// Client shortens URLs: static rules offline first, the mint API otherwise.
type Client struct {
	Base     string // redirector origin, DefaultBase when empty
	Token    string // mint bearer token; empty means static-only
	DynRules []Rule // cached dynamic rules for offline matching (see LoadRuleCache)
	HTTP     *http.Client
}

func (c Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultBase
}

// MintThreshold is the URL length below which minting is skipped: shorter
// URLs never wrap in the panes this tool exists for (decision 0002 item 7),
// and a code buys nothing over a URL that is already short.
const MintThreshold = 40

// Shorten resolves one URL. URLs already shorter than the short form (or not
// shortenable without a token) come back unchanged with Static/Minted false —
// printing something clickable always beats erroring mid-pipeline.
func (c Client) Shorten(long string) (Result, error) {
	if err := ValidTarget(long); err != nil {
		return Result{}, err
	}
	if path := ShortenStatic(long); path != "" {
		return Result{Long: long, Short: c.base() + "/" + path, Static: true}, nil
	}
	if path := ShortenDynamic(c.DynRules, long); path != "" {
		return Result{Long: long, Short: c.base() + "/" + path, Static: true}, nil
	}
	if len(long) < MintThreshold {
		return Result{Long: long, Short: long}, nil
	}
	if c.Token == "" {
		return Result{Long: long, Short: long}, fmt.Errorf("no mint token (set LUKO_TOKEN or keychain item %q)", keychainService)
	}
	short, err := c.mint(long)
	if err != nil {
		return Result{Long: long, Short: long}, err
	}
	return Result{Long: long, Short: short, Minted: true}, nil
}

type mintRequest struct {
	URL string `json:"url"`
}

type mintResponse struct {
	Short string `json:"short"`
	Code  string `json:"code"`
}

func (c Client) mint(long string) (string, error) {
	body, err := json.Marshal(mintRequest{URL: long})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.base()+"/api/mint", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint: %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var out mintResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", err
	}
	if out.Short == "" {
		return "", fmt.Errorf("mint: empty short URL in response")
	}
	return out.Short, nil
}

const keychainService = "luko.to"

// LoadToken resolves the mint token: $LUKO_TOKEN first, then the macOS
// Keychain (service luko.to, account mint). Empty means static-only mode.
func LoadToken() string {
	if t := strings.TrimSpace(os.Getenv("LUKO_TOKEN")); t != "" {
		return t
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", "mint", "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// StoreToken writes the mint token into the macOS Keychain.
func StoreToken(token string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("keychain storage is macOS-only; export LUKO_TOKEN instead")
	}
	cmd := exec.Command("security", "add-generic-password",
		"-s", keychainService, "-a", "mint", "-w", token, "-U")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("security add-generic-password: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OSC8 wraps label as a terminal hyperlink to target (Warp, iTerm2, kitty…).
func OSC8(label, target string) string {
	return "\x1b]8;;" + target + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// --- dynamic-rule API client + local cache -------------------------------

// RuleCachePath is where the CLI caches a redirector's dynamic rules for
// offline matching — one file PER ORIGIN, so a staging --base never
// overwrites the production cache.
func RuleCachePath(origin string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	host := origin
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		host = u.Host
	}
	host = strings.NewReplacer("/", "_", ":", "_").Replace(host)
	return filepath.Join(dir, "shrt", "rules-"+host+".json"), nil
}

// LoadRuleCache reads the origin's cached rules; a missing or corrupt cache
// is just an empty list — the mint path self-heals online.
func LoadRuleCache(origin string) []Rule {
	path, err := RuleCachePath(origin)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rules []Rule
	if json.Unmarshal(data, &rules) != nil {
		return nil
	}
	return rules
}

// SaveRuleCache writes the origin's rule cache.
func SaveRuleCache(origin string, rules []Rule) error {
	path, err := RuleCachePath(origin)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rules, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (c Client) apiDo(method, path string, body any) ([]byte, int, error) {
	if c.Token == "" {
		return nil, 0, fmt.Errorf("no token (set LUKO_TOKEN or keychain item %q)", keychainService)
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, c.base()+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return payload, resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	return payload, resp.StatusCode, nil
}

// FetchRules lists the server's dynamic rules. Callers that want the offline
// cache updated call SaveRuleCache themselves and report failures — a silent
// half-refresh must not masquerade as success.
func (c Client) FetchRules() ([]Rule, error) {
	payload, _, err := c.apiDo(http.MethodGet, "/api/rules", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Rules []Rule `json:"rules"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

// CreateRule adds a dynamic rule server-side and refreshes the cache.
func (c Client) CreateRule(name, prefix string) (Rule, error) {
	payload, _, err := c.apiDo(http.MethodPost, "/api/rules", ruleRequest{Name: name, Prefix: prefix})
	if err != nil {
		return Rule{}, err
	}
	var rule Rule
	if err := json.Unmarshal(payload, &rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

// UpdateRule replaces a rule's prefix server-side and refreshes the cache.
func (c Client) UpdateRule(name, prefix string) (Rule, error) {
	payload, _, err := c.apiDo(http.MethodPut, "/api/rules/"+name, ruleRequest{Prefix: prefix})
	if err != nil {
		return Rule{}, err
	}
	var rule Rule
	if err := json.Unmarshal(payload, &rule); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

// DeleteRule removes a rule server-side.
func (c Client) DeleteRule(name string) error {
	_, _, err := c.apiDo(http.MethodDelete, "/api/rules/"+name, nil)
	return err
}

// IssueToken mints a named member token (admin only). The returned value is
// the only copy that will ever exist.
func (c Client) IssueToken(name string) (TokenIssue, error) {
	payload, _, err := c.apiDo(http.MethodPost, "/api/tokens", tokenIssueRequest{Name: name})
	if err != nil {
		return TokenIssue{}, err
	}
	var out TokenIssue
	if err := json.Unmarshal(payload, &out); err != nil {
		return TokenIssue{}, err
	}
	return out, nil
}

// TokenIssue mirrors the server's issue response.
type TokenIssue struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// RevokeToken deletes a named member token (admin only).
func (c Client) RevokeToken(name string) error {
	_, _, err := c.apiDo(http.MethodDelete, "/api/tokens/"+name, nil)
	return err
}

// ListTokens lists member token names (admin only) — never values.
func (c Client) ListTokens() ([]MemberToken, error) {
	payload, _, err := c.apiDo(http.MethodGet, "/api/tokens", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tokens []MemberToken `json:"tokens"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return out.Tokens, nil
}
