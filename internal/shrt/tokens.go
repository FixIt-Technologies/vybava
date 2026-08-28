package shrt

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MemberToken is a named API credential. Only the SHA-256 of the value is
// stored — the value itself exists once, in the issue response.
type MemberToken struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"` // hex sha256 of the token value
	CreatedAt time.Time `json:"created_at"`
}

// ErrTokenExists reports an issue colliding with an existing name.
var ErrTokenExists = errors.New("token already exists")

// ErrTokenNotFound reports a revoke of a missing name.
var ErrTokenNotFound = errors.New("token not found")

// TokenStore persists member tokens as one JSON file, rewritten atomically.
type TokenStore struct {
	path   string
	mu     sync.RWMutex
	byName map[string]MemberToken
}

// OpenTokenStore loads (or creates) the token store at path.
func OpenTokenStore(path string) (*TokenStore, error) {
	s := &TokenStore{path: path, byName: make(map[string]MemberToken)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var tokens []MemberToken
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, tok := range tokens {
		s.byName[tok.Name] = tok
	}
	return s, nil
}

func (s *TokenStore) persistLocked() error {
	tokens := make([]MemberToken, 0, len(s.byName))
	for _, tok := range s.byName {
		tokens = append(tokens, tok)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Name < tokens[j].Name })
	data, err := json.MarshalIndent(tokens, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Issue creates a named token and returns its value — the only time the
// value ever exists outside the caller's hands.
func (s *TokenStore) Issue(name string) (string, error) {
	if err := ValidateRuleName(name); err != nil { // same shape: short, lowercase, no collisions
		return "", fmt.Errorf("token name: %w", err)
	}
	// The admin identity is derived from the identity STRING — a member
	// token named "admin" would satisfy adminOnly. Ban the name outright.
	if name == adminIdentity {
		return "", fmt.Errorf("token name %q is reserved for the env credential", name)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byName[name]; exists {
		return "", fmt.Errorf("%w: %s", ErrTokenExists, name)
	}
	s.byName[name] = MemberToken{Name: name, Hash: hashToken(value), CreatedAt: time.Now().UTC()}
	if err := s.persistLocked(); err != nil {
		delete(s.byName, name)
		return "", err
	}
	return value, nil
}

// Revoke deletes a named token; its holder loses API access immediately.
func (s *TokenStore) Revoke(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, exists := s.byName[name]
	if !exists {
		return fmt.Errorf("%w: %s", ErrTokenNotFound, name)
	}
	delete(s.byName, name)
	if err := s.persistLocked(); err != nil {
		s.byName[name] = tok
		return err
	}
	return nil
}

// List returns token names and creation times — never hashes or values.
func (s *TokenStore) List() []MemberToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tokens := make([]MemberToken, 0, len(s.byName))
	for _, tok := range s.byName {
		tokens = append(tokens, MemberToken{Name: tok.Name, CreatedAt: tok.CreatedAt})
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Name < tokens[j].Name })
	return tokens
}

// Identify resolves a presented token value to its member name.
func (s *TokenStore) Identify(value string) (string, bool) {
	hash := hashToken(value)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, tok := range s.byName {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(tok.Hash)) == 1 {
			return name, true
		}
	}
	return "", false
}
