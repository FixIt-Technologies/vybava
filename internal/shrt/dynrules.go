package shrt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Rule is a dynamic prefix mapping: any URL starting with Prefix shortens to
// /<Name>/<tail>, and /<Name>/<tail> expands back to Prefix+tail verbatim.
type Rule struct {
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Rule names must never be routable as anything else: not a reserved segment
// or built-in namespace. Collisions with MINTED codes are checked dynamically
// where both stores are visible (rules win route precedence; a rule may not
// take a name an existing code already uses, and mint skips candidates that
// equal a rule name) — a static shape ban would reject ordinary names like
// "oleksandr".
var ruleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,11}$`)

// ValidateRuleName rejects names that could collide with fixed routes.
func ValidateRuleName(name string) error {
	if !ruleNamePattern.MatchString(name) {
		return fmt.Errorf("rule name %q: must match %s (2-12 chars, lowercase)", name, ruleNamePattern)
	}
	if reservedSegments[name] {
		return fmt.Errorf("rule name %q is a reserved segment", name)
	}
	return nil
}

// ValidateRulePrefix rejects prefixes that are not absolute http(s) URLs or
// don't end with "/" — the trailing slash is what makes shorten/expand an
// exact concatenation with no separator guessing.
func ValidateRulePrefix(prefix string) error {
	if err := ValidTarget(prefix); err != nil {
		return err
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("prefix %q must end with \"/\"", prefix)
	}
	return nil
}

// RuleStore persists dynamic rules as one JSON file, rewritten atomically on
// every change — rules are few and updatable, so append-only buys nothing.
type RuleStore struct {
	path   string
	mu     sync.RWMutex
	byName map[string]Rule
}

// OpenRuleStore loads (or creates) the rule store at path.
func OpenRuleStore(path string) (*RuleStore, error) {
	s := &RuleStore{path: path, byName: make(map[string]Rule)}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range rules {
		s.byName[r.Name] = r
	}
	return s, nil
}

func (s *RuleStore) persistLocked() error {
	rules := make([]Rule, 0, len(s.byName))
	for _, r := range s.byName {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	data, err := json.MarshalIndent(rules, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ErrRuleExists reports a create colliding with an existing name.
var ErrRuleExists = errors.New("rule already exists")

// ErrRuleNotFound reports an update/delete of a missing name.
var ErrRuleNotFound = errors.New("rule not found")

// Create adds a new rule; a name already present is an error (use Update).
func (s *RuleStore) Create(name, prefix string) (Rule, error) {
	if err := ValidateRuleName(name); err != nil {
		return Rule{}, err
	}
	if err := ValidateRulePrefix(prefix); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byName[name]; exists {
		return Rule{}, fmt.Errorf("%w: %s", ErrRuleExists, name)
	}
	now := time.Now().UTC()
	rule := Rule{Name: name, Prefix: prefix, CreatedAt: now, UpdatedAt: now}
	s.byName[name] = rule
	if err := s.persistLocked(); err != nil {
		delete(s.byName, name)
		return Rule{}, err
	}
	return rule, nil
}

// Update replaces an existing rule's prefix.
func (s *RuleStore) Update(name, prefix string) (Rule, error) {
	if err := ValidateRulePrefix(prefix); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, exists := s.byName[name]
	if !exists {
		return Rule{}, fmt.Errorf("%w: %s", ErrRuleNotFound, name)
	}
	previous := rule
	rule.Prefix = prefix
	rule.UpdatedAt = time.Now().UTC()
	s.byName[name] = rule
	if err := s.persistLocked(); err != nil {
		s.byName[name] = previous
		return Rule{}, err
	}
	return rule, nil
}

// Delete removes a rule. Existing short links under the name stop resolving —
// that is the caller's deliberate choice, not an accident this API prevents.
func (s *RuleStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, exists := s.byName[name]
	if !exists {
		return fmt.Errorf("%w: %s", ErrRuleNotFound, name)
	}
	delete(s.byName, name)
	if err := s.persistLocked(); err != nil {
		s.byName[name] = rule
		return err
	}
	return nil
}

// List returns all rules sorted by name.
func (s *RuleStore) List() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rules := make([]Rule, 0, len(s.byName))
	for _, r := range s.byName {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules
}

// Get resolves one rule by name.
func (s *RuleStore) Get(name string) (Rule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, ok := s.byName[name]
	return rule, ok
}

// ShortenDynamic returns the short path for a URL covered by a dynamic rule
// (longest matching prefix wins), or "" when none matches. Prefixes end with
// "/" (validated), so the tail never starts with one and expansion is exact
// concatenation.
func ShortenDynamic(rules []Rule, long string) string {
	best := ""
	var bestRule Rule
	for _, r := range rules {
		matchesRoot := long == strings.TrimSuffix(r.Prefix, "/")
		if (matchesRoot || strings.HasPrefix(long, r.Prefix)) && len(r.Prefix) > len(best) {
			best = r.Prefix
			bestRule = r
		}
	}
	if best == "" {
		return ""
	}
	tail := ""
	if long != strings.TrimSuffix(best, "/") {
		tail = long[len(best):]
	}
	if tail == "" {
		return bestRule.Name
	}
	return bestRule.Name + "/" + tail
}

// ExpandDynamic resolves /<name>[/tail] back to the long URL.
func ExpandDynamic(rule Rule, tail string) string {
	if tail == "" {
		return strings.TrimSuffix(rule.Prefix, "/")
	}
	return rule.Prefix + tail
}
