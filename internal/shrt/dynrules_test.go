package shrt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuleNameValidation(t *testing.T) {
	for _, bad := range []string{"gh", "b", "api", "healthz", "x", "UPPER", "7abc", "abcdefg" /* code-shaped */, "with/slash"} {
		if err := ValidateRuleName(bad); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
	for _, good := range []string{"sentry", "plane", "runs", "my-thing", "s1"} {
		if err := ValidateRuleName(good); err != nil {
			t.Errorf("name %q must be accepted: %v", good, err)
		}
	}
}

func TestRulePrefixValidation(t *testing.T) {
	if err := ValidateRulePrefix("https://x.com/a"); err == nil {
		t.Error("prefix without trailing slash must be rejected")
	}
	if err := ValidateRulePrefix("ftp://x.com/a/"); err == nil {
		t.Error("non-http prefix must be rejected")
	}
	if err := ValidateRulePrefix("https://x.com/a/"); err != nil {
		t.Errorf("valid prefix rejected: %v", err)
	}
}

func TestRuleStoreCRUDAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	s, err := OpenRuleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("sentry", "https://sentry.example.com/issues/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("sentry", "https://other.example.com/"); err == nil {
		t.Fatal("duplicate create must fail")
	}
	if _, err := s.Update("sentry", "https://sentry.example.com/org/issues/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("missing", "https://x.com/"); err == nil {
		t.Fatal("update of missing rule must fail")
	}

	reopened, err := OpenRuleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := reopened.Get("sentry")
	if !ok || rule.Prefix != "https://sentry.example.com/org/issues/" {
		t.Fatalf("persisted rule wrong: %+v ok=%v", rule, ok)
	}

	if err := reopened.Delete("sentry"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Delete("sentry"); err == nil {
		t.Fatal("double delete must fail")
	}
	if len(reopened.List()) != 0 {
		t.Fatal("list must be empty after delete")
	}
}

func TestDynamicRoundTrip(t *testing.T) {
	rule := Rule{Name: "sentry", Prefix: "https://sentry.example.com/issues/"}
	long := "https://sentry.example.com/issues/12345"
	short := ShortenDynamic([]Rule{rule}, long)
	if short != "sentry/12345" {
		t.Fatalf("short = %q", short)
	}
	name, tail, _ := strings.Cut(short, "/")
	if name != "sentry" || ExpandDynamic(rule, tail) != long {
		t.Fatalf("roundtrip broken: %q", ExpandDynamic(rule, tail))
	}
	// Bare-name form expands to the prefix page itself.
	if got := ExpandDynamic(rule, ""); got != "https://sentry.example.com/issues" {
		t.Fatalf("bare expand = %q", got)
	}
	// Longest prefix wins.
	specific := Rule{Name: "deep", Prefix: "https://sentry.example.com/issues/special/"}
	if got := ShortenDynamic([]Rule{rule, specific}, "https://sentry.example.com/issues/special/9"); got != "deep/9" {
		t.Fatalf("longest-prefix = %q", got)
	}
}

func newRuleTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := OpenRuleStore(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Base: "https://luko.to", MintToken: "sekrit", Store: store, Rules: rules}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return server, ts
}

func ruleAPICall(t *testing.T, ts *httptest.Server, method, path, body, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, payload
}

func TestServerRuleAPI(t *testing.T) {
	_, ts := newRuleTestServer(t)

	if resp, _ := ruleAPICall(t, ts, "POST", "/api/rules", `{"name":"sentry","prefix":"https://sentry.example.com/issues/"}`, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	resp, body := ruleAPICall(t, ts, "POST", "/api/rules", `{"name":"sentry","prefix":"https://sentry.example.com/issues/"}`, "sekrit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp, _ := ruleAPICall(t, ts, "POST", "/api/rules", `{"name":"sentry","prefix":"https://x.com/"}`, "sekrit"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create: %d", resp.StatusCode)
	}
	if resp, _ := ruleAPICall(t, ts, "POST", "/api/rules", `{"name":"gh","prefix":"https://x.com/"}`, "sekrit"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reserved name: %d", resp.StatusCode)
	}

	// Redirect through the rule, query preserved.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	redirect, err := client.Get(ts.URL + "/sentry/12345?focus=1")
	if err != nil {
		t.Fatal(err)
	}
	defer redirect.Body.Close()
	if loc := redirect.Header.Get("Location"); redirect.StatusCode != http.StatusFound || loc != "https://sentry.example.com/issues/12345?focus=1" {
		t.Fatalf("redirect: %d %q", redirect.StatusCode, loc)
	}

	// Trailing slash in the tail survives the roundtrip (path is not trimmed).
	slashy, err := client.Get(ts.URL + "/sentry/foo/")
	if err != nil {
		t.Fatal(err)
	}
	defer slashy.Body.Close()
	if loc := slashy.Header.Get("Location"); loc != "https://sentry.example.com/issues/foo/" {
		t.Fatalf("trailing slash lost: %q", loc)
	}

	// Mint of a rule-covered URL returns the rule form, no code.
	resp, body = ruleAPICall(t, ts, "POST", "/api/mint", `{"url":"https://sentry.example.com/issues/777"}`, "sekrit")
	var mint mintResponse
	if err := json.Unmarshal(body, &mint); err != nil {
		t.Fatalf("mint decode: %v (%s)", err, body)
	}
	if mint.Short != "https://luko.to/sentry/777" || mint.Code != "" {
		t.Fatalf("mint of rule URL: %+v", mint)
	}

	// Update, then delete, then 404.
	if resp, _ := ruleAPICall(t, ts, "PUT", "/api/rules/sentry", `{"prefix":"https://sentry.example.com/org/"}`, "sekrit"); resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	if resp, _ := ruleAPICall(t, ts, "DELETE", "/api/rules/sentry", "", "sekrit"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := ruleAPICall(t, ts, "DELETE", "/api/rules/sentry", "", "sekrit"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing: %d", resp.StatusCode)
	}
	deleted, err := client.Get(ts.URL + "/sentry/12345")
	if err != nil {
		t.Fatal(err)
	}
	defer deleted.Body.Close()
	if deleted.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted rule must 404, got %d", deleted.StatusCode)
	}
}

func TestClientShortenUsesCachedDynRules(t *testing.T) {
	client := Client{DynRules: []Rule{{Name: "plane", Prefix: "https://plane.example.com/lovinka/projects/"}}}
	result, err := client.Shorten("https://plane.example.com/lovinka/projects/abc-123/issues")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Static || result.Short != DefaultBase+"/plane/abc-123/issues" {
		t.Fatalf("got %+v", result)
	}
}
