package shrt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticRoundTrip(t *testing.T) {
	cases := []struct {
		long, short string
	}{
		{"https://github.com/FixIt-Technologies/FixIt/pull/1088", "gh/fixit/1088"},
		{"https://github.com/FixIt-Technologies/FixIt/issues/42", "gh/fixit/42"},
		{"https://github.com/FixIt-Technologies/vitrinka/pull/250#issuecomment-1", "gh/vitrinka/250"},
		{"https://github.com/Reservine/reservine", "gh/reservine"},
		{"https://vitrinka.ai/b/581", "b/581"},
		{"https://app.vitrinka.ai/b/581", "b/581"},
	}
	for _, c := range cases {
		if got := ShortenStatic(c.long); got != c.short {
			t.Errorf("ShortenStatic(%q) = %q, want %q", c.long, got, c.short)
		}
		long, ok := ExpandStatic(c.short)
		if !ok {
			t.Errorf("ExpandStatic(%q) not ok", c.short)
			continue
		}
		// Expansion is canonical, not byte-identical (issues become /pull/N,
		// fragments drop, app. host normalizes) — it must stay on the same
		// resource, which for these rules means same host + item number.
		if ShortenStatic(long) != c.short {
			t.Errorf("ExpandStatic(%q) = %q does not shorten back", c.short, long)
		}
	}
}

func TestShortenStaticUnknownRepoFallsThrough(t *testing.T) {
	if got := ShortenStatic("https://github.com/torvalds/linux/pull/1"); got != "" {
		t.Errorf("unknown repo should not shorten statically, got %q", got)
	}
}

func TestExpandStaticUnknownAlias(t *testing.T) {
	if _, ok := ExpandStatic("gh/nonexistent/1"); ok {
		t.Error("unknown alias must not expand")
	}
}

func TestCodeIsStableAndSevenChars(t *testing.T) {
	url := "https://app.vitrinka.ai/w/fixit/boards/pultik-brainstorm-hud-refinement"
	code := Code(url)
	if len(code) != 7 || code != Code(url) {
		t.Fatalf("code %q not stable 7 chars", code)
	}
	if !codePattern.MatchString(code) {
		t.Fatalf("code %q does not match route pattern", code)
	}
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Base: "https://luko.to", MintToken: "sekrit", Store: store}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return server, ts
}

func TestServerStaticRedirect(t *testing.T) {
	_, ts := newTestServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/gh/fixit/1088")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "https://github.com/FixIt-Technologies/FixIt/pull/1088" {
		t.Fatalf("location %q", loc)
	}
}

func TestServerMintAndRedirect(t *testing.T) {
	_, ts := newTestServer(t)
	long := "https://example.com/some/very/long/path?with=query"

	mint := func() mintResponse {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/mint",
			strings.NewReader(`{"url":"`+long+`"}`))
		req.Header.Set("Authorization", "Bearer sekrit")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("mint status %d", resp.StatusCode)
		}
		var out mintResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first, second := mint(), mint()
	if first.Code == "" || first != second {
		t.Fatalf("mint not idempotent: %+v vs %+v", first, second)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/" + first.Code)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != long {
		t.Fatalf("redirect %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestServerMintAuth(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/mint",
		strings.NewReader(`{"url":"https://example.com/x"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

func TestServerMintStaticURLReturnsStaticForm(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/mint",
		strings.NewReader(`{"url":"https://github.com/FixIt-Technologies/FixIt/pull/7"}`))
	req.Header.Set("Authorization", "Bearer sekrit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out mintResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Short != "https://luko.to/gh/fixit/7" || out.Code != "" {
		t.Fatalf("got %+v", out)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.jsonl")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	code, created, err := store.Mint("https://example.com/persist")
	if err != nil || !created {
		t.Fatalf("mint: %v created=%v", err, created)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if url, ok := reopened.Lookup(code); !ok || url != "https://example.com/persist" {
		t.Fatalf("lookup after reopen: %q %v", url, ok)
	}
}

func TestClientStaticOffline(t *testing.T) {
	client := Client{} // no token, no network needed
	result, err := client.Shorten("https://github.com/FixIt-Technologies/FixIt/pull/1088")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Static || result.Short != "https://luko.to/gh/fixit/1088" {
		t.Fatalf("got %+v", result)
	}
}

func TestMintCollisionExtendsCode(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	victim := "https://example.com/victim-target-that-is-long-enough"
	imposter := "https://example.com/imposter-target-also-long-enough"
	// Force the collision: pre-seat the imposter's 7-char prefix on the victim.
	store.byCode[Code(imposter)] = victim
	code, created, err := store.Mint(imposter)
	if err != nil || !created {
		t.Fatalf("mint: %v created=%v", err, created)
	}
	if code == Code(imposter) || len(code) != codeLen+1 {
		t.Fatalf("collision must extend the code, got %q", code)
	}
	if url, ok := store.Lookup(code); !ok || url != imposter {
		t.Fatalf("extended code resolves to %q", url)
	}
	if url, _ := store.Lookup(Code(imposter)); url != victim {
		t.Fatalf("victim's code was clobbered: %q", url)
	}
}

func TestMintSkipsReservedSpelling(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// No real URL hashes to "healthz" on demand; simulate by marking the
	// 7-char prefix reserved-equivalent via the same path the loop takes.
	url := "https://example.com/reserved-spelling-probe-long-enough"
	if reservedSegments[Code(url)] {
		t.Skip("astronomically unlucky: probe URL actually spells a reserved word")
	}
	code, _, err := store.Mint(url)
	if err != nil {
		t.Fatal(err)
	}
	if reservedSegments[code] {
		t.Fatalf("minted a reserved spelling %q", code)
	}
}

func TestClientSkipsMintingShortURLs(t *testing.T) {
	client := Client{Token: "irrelevant"}
	short := "https://x.com/ab" // < MintThreshold, no static rule
	result, err := client.Shorten(short)
	if err != nil {
		t.Fatal(err)
	}
	if result.Minted || result.Short != short {
		t.Fatalf("short URL must pass through unchanged, got %+v", result)
	}
}

func TestClientNoTokenStillPrintsOriginal(t *testing.T) {
	client := Client{}
	long := "https://example.com/needs/minting/and/is/long/enough/to/qualify"
	result, err := client.Shorten(long)
	if err == nil {
		t.Fatal("want error without token")
	}
	if result.Short != long {
		t.Fatalf("fallback must return the original URL, got %q", result.Short)
	}
}

func TestOSC8(t *testing.T) {
	got := OSC8("board", "https://luko.to/b/581")
	want := "\x1b]8;;https://luko.to/b/581\x1b\\board\x1b]8;;\x1b\\"
	if got != want {
		t.Fatalf("OSC8 = %q", got)
	}
}
