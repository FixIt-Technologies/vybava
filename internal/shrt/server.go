package shrt

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Store persists minted links as append-only JSONL with an in-memory map.
// Codes are deterministic hashes of the URL, so concurrent mints of the same
// URL are naturally idempotent.
type Store struct {
	path   string
	mu     sync.RWMutex
	byCode map[string]string
}

type storeRecord struct {
	Code string    `json:"code"`
	URL  string    `json:"url"`
	At   time.Time `json:"at"`
}

// OpenStore loads (or creates) the JSONL store at path.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, byCode: make(map[string]string)}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec storeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("%s: corrupt line: %w", path, err)
		}
		s.byCode[rec.Code] = rec.URL
	}
	return s, scanner.Err()
}

// maxLinks bounds the in-memory store. Minting is token-authed, so this is a
// backstop against a runaway minter, not an attacker.
const maxLinks = 100_000

// Mint records url under its deterministic code and reports whether the code
// was newly created. A 7-char prefix that already maps to a DIFFERENT url (or
// spells a reserved path segment) extends until it is free or matches — a
// truncated-hash collision must never redirect to the wrong target.
func (s *Store) Mint(url string) (code string, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = ""
	for n := codeLen; n <= maxCodeLen; n++ {
		candidate := CodeN(url, n)
		if reservedSegments[candidate] {
			continue
		}
		existing, exists := s.byCode[candidate]
		if exists && existing != url {
			continue
		}
		if exists {
			return candidate, false, nil
		}
		code = candidate
		break
	}
	if code == "" {
		return "", false, fmt.Errorf("code space exhausted for %q", url)
	}
	if len(s.byCode) >= maxLinks {
		return "", false, fmt.Errorf("store at capacity (%d links)", maxLinks)
	}
	line, err := json.Marshal(storeRecord{Code: code, URL: url, At: time.Now().UTC()})
	if err != nil {
		return "", false, err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return "", false, err
	}
	s.byCode[code] = url
	return code, true, nil
}

// Lookup resolves a minted code.
func (s *Store) Lookup(code string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	url, ok := s.byCode[code]
	return url, ok
}

// Server is the luko.to redirector.
type Server struct {
	Base      string // public origin used in mint responses
	MintToken string // ADMIN bearer token; empty disables the whole API
	Store     *Store
	Rules     *RuleStore
	Tokens    *TokenStore // named member tokens (mint + rules, not token management)
	Log       *log.Logger
}

// adminIdentity is the identity of the env-token holder.
const adminIdentity = "admin"

// identify resolves the request's bearer token to an identity name.
func (s *Server) identify(r *http.Request) (string, bool) {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || value == "" {
		return "", false
	}
	if subtleEqual(value, s.MintToken) {
		return adminIdentity, true
	}
	if s.Tokens != nil {
		return s.Tokens.Identify(value)
	}
	return "", false
}

func subtleEqual(a, b string) bool {
	return len(a) == len(b) && hashToken(a) == hashToken(b)
}

var codePattern = regexp.MustCompile(`^[a-z2-7]{7,52}$`)

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

// Handler routes: static namespaces, then 7-char minted codes, then 404.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /api/mint", s.authed(s.handleMint))
	mux.HandleFunc("GET /api/rules", s.authed(s.handleRuleList))
	mux.HandleFunc("POST /api/rules", s.authed(s.handleRuleCreate))
	mux.HandleFunc("PUT /api/rules/{name}", s.authed(s.handleRuleUpdate))
	mux.HandleFunc("DELETE /api/rules/{name}", s.authed(s.handleRuleDelete))
	mux.HandleFunc("GET /api/tokens", s.adminOnly(s.handleTokenList))
	mux.HandleFunc("POST /api/tokens", s.adminOnly(s.handleTokenIssue))
	mux.HandleFunc("DELETE /api/tokens/{name}", s.adminOnly(s.handleTokenRevoke))
	mux.HandleFunc("/", s.handleRedirect)
	return mux
}

// identityHandler is an API handler that knows who is calling.
type identityHandler func(w http.ResponseWriter, r *http.Request, who string)

// authed gates an API handler behind any valid token (admin or member).
func (s *Server) authed(next identityHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.MintToken == "" {
			http.Error(w, "api disabled", http.StatusForbidden)
			return
		}
		who, ok := s.identify(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r, who)
	}
}

// adminOnly gates token management behind the admin env token alone —
// members can mint and manage rules, never each other's access.
func (s *Server) adminOnly(next identityHandler) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, who string) {
		if who != adminIdentity {
			http.Error(w, "admin token required", http.StatusForbidden)
			return
		}
		next(w, r, who)
	})
}

type tokenIssueRequest struct {
	Name string `json:"name"`
}

type tokenIssueResponse struct {
	Name  string `json:"name"`
	Token string `json:"token"` // shown exactly once
}

func (s *Server) handleTokenList(w http.ResponseWriter, _ *http.Request, _ string) {
	if s.Tokens == nil {
		http.Error(w, "tokens unavailable", http.StatusServiceUnavailable)
		return
	}
	writeAPIJSON(w, map[string]any{"tokens": s.Tokens.List()})
}

func (s *Server) handleTokenIssue(w http.ResponseWriter, r *http.Request, _ string) {
	if s.Tokens == nil {
		http.Error(w, "tokens unavailable", http.StatusServiceUnavailable)
		return
	}
	var req tokenIssueRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	value, err := s.Tokens.Issue(strings.TrimSpace(req.Name))
	if errors.Is(err, ErrTokenExists) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logf("token issued: %s", req.Name)
	writeAPIJSON(w, tokenIssueResponse{Name: strings.TrimSpace(req.Name), Token: value})
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request, _ string) {
	if s.Tokens == nil {
		http.Error(w, "tokens unavailable", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	err := s.Tokens.Revoke(name)
	if errors.Is(err, ErrTokenNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logf("token revoked: %s", name)
	w.WriteHeader(http.StatusNoContent)
}

type ruleRequest struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

func (s *Server) handleRuleList(w http.ResponseWriter, _ *http.Request, _ string) {
	if s.Rules == nil {
		http.Error(w, "rules unavailable", http.StatusServiceUnavailable)
		return
	}
	writeAPIJSON(w, map[string]any{"rules": s.Rules.List()})
}

func (s *Server) handleRuleCreate(w http.ResponseWriter, r *http.Request, who string) {
	if s.Rules == nil {
		http.Error(w, "rules unavailable", http.StatusServiceUnavailable)
		return
	}
	var req ruleRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	rule, err := s.Rules.Create(strings.TrimSpace(req.Name), strings.TrimSpace(req.Prefix))
	if errors.Is(err, ErrRuleExists) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logf("rule created: %s -> %s (by %s)", rule.Name, rule.Prefix, who)
	writeAPIJSON(w, rule)
}

func (s *Server) handleRuleUpdate(w http.ResponseWriter, r *http.Request, who string) {
	if s.Rules == nil {
		http.Error(w, "rules unavailable", http.StatusServiceUnavailable)
		return
	}
	var req ruleRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	rule, err := s.Rules.Update(r.PathValue("name"), strings.TrimSpace(req.Prefix))
	if errors.Is(err, ErrRuleNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.logf("rule updated: %s -> %s (by %s)", rule.Name, rule.Prefix, who)
	writeAPIJSON(w, rule)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request, who string) {
	if s.Rules == nil {
		http.Error(w, "rules unavailable", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	err := s.Rules.Delete(name)
	if errors.Is(err, ErrRuleNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logf("rule deleted: %s (by %s)", name, who)
	w.WriteHeader(http.StatusNoContent)
}

func writeAPIJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("shrt: writing api response: %v", err)
	}
}

func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		fmt.Fprintln(w, "luko.to — personal short links")
		return
	}
	if long, ok := ExpandStatic(path); ok {
		http.Redirect(w, r, long, http.StatusFound)
		return
	}
	// Dynamic rules: /<name>[/tail]. Names are validated to never collide
	// with reserved segments or code-shaped strings, so order is safe. The
	// tail comes from the UNTRIMMED path — a trailing slash is part of the
	// target URL and must survive the roundtrip.
	if s.Rules != nil {
		name, tail, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if rule, ok := s.Rules.Get(name); ok {
			long := ExpandDynamic(rule, tail)
			if q := r.URL.RawQuery; q != "" {
				long += "?" + q
			}
			http.Redirect(w, r, long, http.StatusFound)
			return
		}
	}
	if codePattern.MatchString(path) {
		if long, ok := s.Store.Lookup(path); ok {
			http.Redirect(w, r, long, http.StatusFound)
			return
		}
	}
	s.logf("miss: %s", path)
	http.NotFound(w, r)
}

func (s *Server) handleMint(w http.ResponseWriter, r *http.Request, who string) {
	var req mintRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := ValidTarget(req.URL); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// A static- or dynamic-rule URL never needs a code; answer with the rule
	// form so stale-cached CLIs still get the canonical short link.
	if path := ShortenStatic(req.URL); path != "" {
		writeMintResponse(w, mintResponse{Short: s.Base + "/" + path})
		return
	}
	if s.Rules != nil {
		if path := ShortenDynamic(s.Rules.List(), req.URL); path != "" {
			writeMintResponse(w, mintResponse{Short: s.Base + "/" + path})
			return
		}
	}
	code, created, err := s.Store.Mint(req.URL)
	if err != nil {
		s.logf("mint failed: %v", err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if created {
		s.logf("minted %s -> %s (by %s)", code, req.URL, who)
	}
	writeMintResponse(w, mintResponse{Short: s.Base + "/" + code, Code: code})
}

func writeMintResponse(w http.ResponseWriter, resp mintResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Headers are gone; nothing recoverable, but never swallow silently.
		log.Printf("shrt: writing mint response: %v", err)
	}
}
