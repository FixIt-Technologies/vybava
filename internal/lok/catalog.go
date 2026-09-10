// Package lok owns locale catalogs for AI-driven sessions: a JSON catalog is
// never read whole — it is queried by key, written by verb, and kept in sync
// across locales by construction. Configured through the `lok` section of
// vybava.config.ts (see internal/vconfig).
package lok

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Style is a catalog's key style.
type Style string

const (
	// StyleEnglishAsKey — the key is the English source text; an `en` file maps key → key.
	StyleEnglishAsKey Style = "english-as-key"
	// StylePath — nested JSON addressed by dotted path.
	StylePath Style = "path"
)

var defaultPlurals = []string{"_zero", "_one", "_two", "_few", "_many", "_other"}

// ScanConfig is the source scan for english-as-key catalogs.
type ScanConfig struct {
	Roots      []string `json:"roots"`
	Call       string   `json:"call,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// CatalogConfig mirrors the TypeScript CatalogConfig.
type CatalogConfig struct {
	Style      Style       `json:"style"`
	Files      string      `json:"files"`
	Locales    []string    `json:"locales"`
	Required   []string    `json:"required,omitempty"`
	Plurals    []string    `json:"plurals,omitempty"`
	AfterWrite string      `json:"afterWrite,omitempty"`
	Scan       *ScanConfig `json:"scan,omitempty"`
}

// Config is the `lok` section.
type Config struct {
	Catalogs map[string]CatalogConfig `json:"catalogs"`
}

// Validate checks the shape once so every verb can trust it.
func (c *Config) Validate() error {
	if len(c.Catalogs) == 0 {
		return errors.New("lok.catalogs is empty")
	}
	for id, cat := range c.Catalogs {
		switch cat.Style {
		case StyleEnglishAsKey, StylePath:
		default:
			return fmt.Errorf("catalog %q: style must be english-as-key or path, got %q", id, cat.Style)
		}
		if !strings.Contains(cat.Files, "{locale}") {
			return fmt.Errorf("catalog %q: files must contain {locale}", id)
		}
		if len(cat.Locales) == 0 {
			return fmt.Errorf("catalog %q: locales is empty", id)
		}
		for _, r := range cat.Required {
			if !contains(cat.Locales, r) {
				return fmt.Errorf("catalog %q: required locale %q is not in locales", id, r)
			}
		}
		if cat.Scan != nil && cat.Style != StyleEnglishAsKey {
			return fmt.Errorf("catalog %q: scan is only supported for english-as-key catalogs", id)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Required returns the locales every key must carry.
func (c CatalogConfig) RequiredLocales() []string {
	if len(c.Required) > 0 {
		return c.Required
	}
	return c.Locales
}

// PluralSuffixes returns the configured or default plural suffixes.
func (c CatalogConfig) PluralSuffixes() []string {
	if len(c.Plurals) > 0 {
		return c.Plurals
	}
	return defaultPlurals
}

// BaseKey strips a plural suffix: "{{count}} hour_one" → "{{count}} hour".
func (c CatalogConfig) BaseKey(key string) (base string, plural bool) {
	for _, s := range c.PluralSuffixes() {
		if strings.HasSuffix(key, s) && len(key) > len(s) {
			return strings.TrimSuffix(key, s), true
		}
	}
	return key, false
}

// FilePath resolves the catalog file for one locale.
func (c CatalogConfig) FilePath(root, locale string) string {
	return filepath.Join(root, strings.ReplaceAll(c.Files, "{locale}", locale))
}

// ---------------------------------------------------------------------------
// Ordered JSON: catalogs keep their key order, and inserts go to the sorted
// slot without ever reordering existing keys — a diff shows only the change.
// ---------------------------------------------------------------------------

// Entry is one key/value pair of an ordered object; Value is a string leaf
// or a nested *Object.
type Entry struct {
	Key   string
	Value any
}

// Object is an insertion-ordered JSON object.
type Object struct {
	Entries []Entry
}

// Get returns the entry index for key, or -1.
func (o *Object) index(key string) int {
	for i, e := range o.Entries {
		if e.Key == key {
			return i
		}
	}
	return -1
}

// Get looks up a direct child.
func (o *Object) Get(key string) (any, bool) {
	if i := o.index(key); i >= 0 {
		return o.Entries[i].Value, true
	}
	return nil, false
}

// less is the insertion order for new keys: case-insensitive, then exact.
func less(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// Set replaces an existing child in place or inserts a new one at the first
// slot whose key sorts after it (keys after that slot are left untouched even
// if they are out of order — we never reorder what we did not write).
func (o *Object) Set(key string, value any) {
	if i := o.index(key); i >= 0 {
		o.Entries[i].Value = value
		return
	}
	at := len(o.Entries)
	for i, e := range o.Entries {
		if less(key, e.Key) {
			at = i
			break
		}
	}
	o.Entries = append(o.Entries, Entry{})
	copy(o.Entries[at+1:], o.Entries[at:])
	o.Entries[at] = Entry{Key: key, Value: value}
}

// Delete removes a direct child; reports whether it existed.
func (o *Object) Delete(key string) bool {
	i := o.index(key)
	if i < 0 {
		return false
	}
	o.Entries = append(o.Entries[:i], o.Entries[i+1:]...)
	return true
}

// ParseObject decodes a JSON object preserving key order. Only string leaves
// and nested objects are allowed — a catalog is text, nothing else.
func ParseObject(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("catalog root is not an object")
	}
	obj, err := parseObjectBody(dec, "")
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing content after the catalog object")
	}
	return obj, nil
}

func parseObjectBody(dec *json.Decoder, path string) (*Object, error) {
	obj := &Object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("at %q: non-string key %v", path, tok)
		}
		full := key
		if path != "" {
			full = path + "." + key
		}
		val, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch v := val.(type) {
		case string:
			obj.Entries = append(obj.Entries, Entry{Key: key, Value: v})
		case json.Delim:
			if v != '{' {
				return nil, fmt.Errorf("at %q: arrays are not supported in catalogs", full)
			}
			child, err := parseObjectBody(dec, full)
			if err != nil {
				return nil, err
			}
			obj.Entries = append(obj.Entries, Entry{Key: key, Value: child})
		default:
			return nil, fmt.Errorf("at %q: value must be a string or object, got %v", full, val)
		}
	}
	if _, err := dec.Token(); err != nil { // closing }
		return nil, err
	}
	return obj, nil
}

// Marshal renders the object with two-space indent and a trailing newline,
// the shape JSON.stringify(o, null, 2) + "\n" produces.
func (o *Object) Marshal() []byte {
	var b bytes.Buffer
	writeObject(&b, o, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

func writeObject(b *bytes.Buffer, o *Object, depth int) {
	if len(o.Entries) == 0 {
		b.WriteString("{}")
		return
	}
	indent := strings.Repeat("  ", depth+1)
	b.WriteString("{\n")
	for i, e := range o.Entries {
		b.WriteString(indent)
		b.Write(jsonString(e.Key))
		b.WriteString(": ")
		switch v := e.Value.(type) {
		case string:
			b.Write(jsonString(v))
		case *Object:
			writeObject(b, v, depth+1)
		}
		if i < len(o.Entries)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte('}')
}

// jsonString encodes like JSON.stringify: no HTML escaping, UTF-8 kept.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Leaves flattens to dotted-path → value in file order.
func (o *Object) Leaves() []Entry {
	var out []Entry
	var walk func(*Object, string)
	walk = func(x *Object, prefix string) {
		for _, e := range x.Entries {
			p := e.Key
			if prefix != "" {
				p = prefix + "." + e.Key
			}
			switch v := e.Value.(type) {
			case string:
				out = append(out, Entry{Key: p, Value: v})
			case *Object:
				walk(v, p)
			}
		}
	}
	walk(o, "")
	return out
}

// ---------------------------------------------------------------------------
// Catalog files
// ---------------------------------------------------------------------------

// Locale is one loaded locale file.
type Locale struct {
	Code   string
	Path   string
	Object *Object
	Exists bool
}

// Catalog is one configured catalog with every locale loaded.
type Catalog struct {
	ID      string
	Root    string
	Config  CatalogConfig
	Locales map[string]*Locale
}

// LoadCatalog reads every locale file (a missing file is an empty locale
// flagged Exists=false so `check` can report it).
func LoadCatalog(root, id string, cfg CatalogConfig) (*Catalog, error) {
	c := &Catalog{ID: id, Root: root, Config: cfg, Locales: map[string]*Locale{}}
	for _, code := range cfg.Locales {
		p := cfg.FilePath(root, code)
		loc := &Locale{Code: code, Path: p, Object: &Object{}}
		data, err := os.ReadFile(p)
		switch {
		case err == nil:
			obj, err := ParseObject(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			loc.Object, loc.Exists = obj, true
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, err
		}
		c.Locales[code] = loc
	}
	return c, nil
}

// splitPath addresses nested catalogs; flat catalogs use the whole key.
func (c *Catalog) splitPath(key string) []string {
	if c.Config.Style == StylePath {
		return strings.Split(key, ".")
	}
	return []string{key}
}

// Lookup returns the value of key in one locale.
func (c *Catalog) Lookup(locale, key string) (string, bool) {
	loc, ok := c.Locales[locale]
	if !ok {
		return "", false
	}
	cur := loc.Object
	parts := c.splitPath(key)
	for i, p := range parts {
		v, ok := cur.Get(p)
		if !ok {
			return "", false
		}
		if i == len(parts)-1 {
			s, ok := v.(string)
			return s, ok
		}
		child, ok := v.(*Object)
		if !ok {
			return "", false
		}
		cur = child
	}
	return "", false
}

// Put sets key in one locale, creating intermediate objects for path style.
func (c *Catalog) Put(locale, key, value string) error {
	loc, ok := c.Locales[locale]
	if !ok {
		return fmt.Errorf("catalog %q has no locale %q", c.ID, locale)
	}
	cur := loc.Object
	parts := c.splitPath(key)
	for _, p := range parts[:len(parts)-1] {
		v, ok := cur.Get(p)
		if !ok {
			child := &Object{}
			cur.Set(p, child)
			cur = child
			continue
		}
		child, ok := v.(*Object)
		if !ok {
			return fmt.Errorf("%s: %q is a string, cannot nest %q under it", locale, p, key)
		}
		cur = child
	}
	if v, ok := cur.Get(parts[len(parts)-1]); ok {
		if _, isObj := v.(*Object); isObj {
			return fmt.Errorf("%s: %q is an object, not a string leaf", locale, key)
		}
	}
	cur.Set(parts[len(parts)-1], value)
	loc.Exists = true
	return nil
}

// Remove deletes key from one locale; reports whether it existed.
func (c *Catalog) Remove(locale, key string) bool {
	loc, ok := c.Locales[locale]
	if !ok {
		return false
	}
	cur := loc.Object
	parts := c.splitPath(key)
	for _, p := range parts[:len(parts)-1] {
		v, ok := cur.Get(p)
		if !ok {
			return false
		}
		child, ok := v.(*Object)
		if !ok {
			return false
		}
		cur = child
	}
	return cur.Delete(parts[len(parts)-1])
}

// Keys returns the union of leaf keys across locales, in the order of the
// first locale that has them.
func (c *Catalog) Keys() []string {
	seen := map[string]bool{}
	var out []string
	for _, code := range c.Config.Locales {
		for _, e := range c.Locales[code].Object.Leaves() {
			if !seen[e.Key] {
				seen[e.Key] = true
				out = append(out, e.Key)
			}
		}
	}
	return out
}

// Save writes every locale whose object was touched. Files are written whole
// but only the changed keys differ, because order is preserved.
func (c *Catalog) Save() ([]string, error) {
	var written []string
	for _, code := range c.Config.Locales {
		loc := c.Locales[code]
		if !loc.Exists {
			continue
		}
		data := loc.Object.Marshal()
		if old, err := os.ReadFile(loc.Path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(loc.Path), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(loc.Path, data, 0o644); err != nil {
			return written, err
		}
		written = append(written, loc.Path)
	}
	sort.Strings(written)
	return written, nil
}
