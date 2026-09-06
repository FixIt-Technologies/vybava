package plaud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T, mux *http.ServeMux) Config {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return Config{
		ClientID:   "client_test",
		TokenURL:   server.URL + "/token",
		RefreshURL: server.URL + "/refresh",
		APIBase:    server.URL + "/api",
		CacheFile:  filepath.Join(t.TempDir(), "access-token.json"),
		HTTP:       server.Client(),
		Now:        func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func jsonResponse(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// TestLoginExchangesCallbackCode drives the whole PKCE flow against a fake
// token endpoint: the callback with the right state triggers the exchange
// with Basic "<client_id>:" auth, and the raw token JSON comes back.
func TestLoginExchangesCallbackCode(t *testing.T) {
	var form map[string][]string
	var auth string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form, auth = r.PostForm, r.Header.Get("Authorization")
		jsonResponse(w, 200, `{"access_token":"at-SECRET","refresh_token":"rt-SECRET","token_type":"Bearer","expires_in":3600,"extra":"kept"}`)
	})
	cfg := testConfig(t, mux)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RedirectURI = "http://" + listener.Addr().String() + "/auth/callback"
	listener.Close()

	var progress strings.Builder
	open := func(consent string) error {
		u := mustURL(t, consent)
		if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("client_id") != "client_test" {
			t.Errorf("consent URL missing PKCE/client params: %s", consent)
		}
		go func() {
			// Wrong state first — code and error alike — must be ignored, not settle the flow.
			_, _ = http.Get(cfg.RedirectURI + "?code=bogus&state=nope")
			_, _ = http.Get(cfg.RedirectURI + "?error=access_denied&error_description=forged")
			_, _ = http.Get(cfg.RedirectURI + "?code=abc&state=" + u.Query().Get("state"))
		}()
		return nil
	}
	token, err := cfg.Login(context.Background(), LoginOptions{Open: open, Progress: &progress, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token.RefreshToken != "rt-SECRET" || token.Raw["extra"] != "kept" {
		t.Errorf("token = %+v, want refresh rt and unknown fields kept", token)
	}
	if form["code"][0] != "abc" || form["grant_type"][0] != "authorization_code" || form["code_verifier"][0] == "" {
		t.Errorf("exchange form = %v", form)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("client_test:")); auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
	if strings.Contains(progress.String(), "SECRET") {
		t.Errorf("progress leaked token material: %s", progress.String())
	}
}

// TestSessionRefreshesAndCachesAccessToken pins the cache contract: a
// refresh writes ONLY the access token (0600), a rotated refresh token is
// reported through Rotated, the cache short-circuits the next call, and a
// different injected credential never reads it.
func TestSessionRefreshesAndCachesAccessToken(t *testing.T) {
	refreshes := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		_ = r.ParseForm()
		if rt := r.PostForm.Get("refresh_token"); rt != "old" && rt != "other" {
			t.Errorf("refresh_token = %q", rt)
		}
		jsonResponse(w, 200, `{"access_token":"fresh","refresh_token":"rotated","expires_in":3600}`)
	})
	cfg := testConfig(t, mux)
	var rotated Token
	session := Session{Config: cfg, RefreshToken: "old", Rotated: func(tk Token) { rotated = tk }}

	for i := 0; i < 2; i++ {
		token, err := session.AccessToken(context.Background())
		if err != nil {
			t.Fatalf("AccessToken #%d: %v", i, err)
		}
		if token != "fresh" {
			t.Errorf("token = %q", token)
		}
	}
	if refreshes != 1 {
		t.Errorf("refreshes = %d, want 1 (second call served from cache)", refreshes)
	}
	if _, err := (Session{Config: cfg, RefreshToken: "other"}).AccessToken(context.Background()); err != nil || refreshes != 2 {
		t.Errorf("other credential: err %v, refreshes = %d, want 2 (cache is bound to the credential)", err, refreshes)
	}
	if rotated.RefreshToken != "rotated" {
		t.Errorf("Rotated not reported: %+v", rotated)
	}
	raw, err := os.ReadFile(cfg.CacheFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rotated") || strings.Contains(string(raw), "old") {
		t.Errorf("refresh token leaked into cache: %s", raw)
	}
	if info, _ := os.Stat(cfg.CacheFile); info.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %o, want 600", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(cfg.CacheFile)); len(entries) != 1 {
		t.Errorf("cache dir holds %d entries, want only the cache file (temp file leaked)", len(entries))
	}
}

// TestCacheFailureNeverHidesRotation makes the cache best-effort: with an
// unwritable cache path the refreshed token is still returned, the rotation
// notice still fires, and the write error surfaces through Warn.
func TestCacheFailureNeverHidesRotation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, `{"access_token":"fresh","refresh_token":"rotated","expires_in":3600}`)
	})
	cfg := testConfig(t, mux)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.CacheFile = filepath.Join(blocker, "access-token.json")
	var rotated, warned bool
	session := Session{Config: cfg, RefreshToken: "old", Rotated: func(Token) { rotated = true }, Warn: func(error) { warned = true }}
	token, err := session.AccessToken(context.Background())
	if err != nil || token != "fresh" {
		t.Fatalf("AccessToken = %q, %v; want the refreshed token despite the cache failure", token, err)
	}
	if !rotated || !warned {
		t.Errorf("rotated = %v, warned = %v; want both", rotated, warned)
	}
}

func TestRefreshRejectedIsActionable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 401, `{"detail":"Invalid refresh token"}`)
	})
	cfg := testConfig(t, mux)
	_, err := Session{Config: cfg, RefreshToken: "dead"}.AccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "plaud login") || !strings.Contains(err.Error(), "Invalid refresh token") {
		t.Errorf("err = %v, want login hint with server detail", err)
	}
	if _, err := (Session{Config: cfg}).AccessToken(context.Background()); err != ErrNoRefreshToken {
		t.Errorf("missing env err = %v", err)
	}
}

// TestListFilesQueryScansPages mirrors the MCP: a query walks 100-item pages
// and keeps case-insensitive name matches.
func TestListFilesQueryScansPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/open/third-party/files/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			jsonResponse(w, 401, `{"detail":"nope"}`)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			items := make([]string, 0, filterPageSize)
			for i := 0; i < filterPageSize; i++ {
				items = append(items, fmt.Sprintf(`{"id":"f%d","name":"Recording %d"}`, i, i))
			}
			jsonResponse(w, 200, `{"data":[`+strings.Join(items, ",")+`]}`)
		default:
			jsonResponse(w, 200, `{"data":[{"id":"w","name":"WEEKLY sync"}]}`)
		}
	})
	cfg := testConfig(t, mux)
	if err := (Session{Config: cfg}).CacheAccessToken(Token{AccessToken: "at", ExpiresIn: 3600}); err != nil {
		t.Fatal(err)
	}
	client := Client{Session: Session{Config: cfg}}
	list, err := client.ListFiles(context.Background(), ListOptions{Query: "weekly"})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if list.Scanned != filterPageSize+1 || list.Matched != 1 || list.Data[0]["id"] != "w" {
		t.Errorf("list = scanned %d matched %d data %v", list.Scanned, list.Matched, list.Data)
	}
}

// TestTranscriptFollowsDataLink resolves the requested block via its
// presigned link and parses the utterance list; a missing block names the
// available ones.
func TestTranscriptFollowsDataLink(t *testing.T) {
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/api/open/third-party/files/abc", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, `{"id":"abc","name":"Standup","source_list":[
			{"data_type":"transaction","data_link":"`+base+`/blob"},
			{"data_type":"outline","data_content":"- point"}],"note_list":[{"title":"Summary"}]}`)
	})
	mux.HandleFunc("/blob", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, `[{"speaker":"Lukáš","start_time":0,"content":"ahoj"},{"speaker":"Eva","start_time":1500,"content":"čau"}]`)
	})
	cfg := testConfig(t, mux)
	base = cfg.APIBase[:len(cfg.APIBase)-len("/api")]
	if err := (Session{Config: cfg}).CacheAccessToken(Token{AccessToken: "at", ExpiresIn: 3600}); err != nil {
		t.Fatal(err)
	}
	client := Client{Session: Session{Config: cfg}}

	tr, err := client.Transcript(context.Background(), "abc", "")
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if tr.Total != 2 || tr.Segments[1]["content"] != "čau" || strings.Join(tr.Available, ",") != "transaction,outline" {
		t.Errorf("transcript = %+v", tr)
	}
	if _, err := client.Transcript(context.Background(), "abc", "transaction_polish"); err == nil || !strings.Contains(err.Error(), "transaction, outline") {
		t.Errorf("missing block err = %v", err)
	}
	file, err := client.GetFile(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	var notes []map[string]any
	if json.Unmarshal(file.NoteList, &notes) != nil || len(notes) != 1 || file.Raw["name"] != "Standup" {
		t.Errorf("file = %+v", file)
	}
}

// TestExpiredCacheRefreshes ensures a 401 from the API clears a stale cache
// and retries once with a refreshed token.
func TestExpiredCacheRefreshes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/refresh", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, `{"access_token":"new","expires_in":3600}`)
	})
	mux.HandleFunc("/api/open/third-party/users/current", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			jsonResponse(w, 401, `{"detail":"expired"}`)
			return
		}
		jsonResponse(w, 200, `{"email":"me@example.com"}`)
	})
	cfg := testConfig(t, mux)
	if err := (Session{Config: cfg, RefreshToken: "rt"}).CacheAccessToken(Token{AccessToken: "stale", ExpiresIn: 3600}); err != nil {
		t.Fatal(err)
	}
	user, err := Client{Session: Session{Config: cfg, RefreshToken: "rt"}}.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if user["email"] != "me@example.com" {
		t.Errorf("user = %v", user)
	}
}

// TestOversizedBodyIsAnError pins that a block bigger than the limit fails
// loudly instead of coming back truncated as plain text.
func TestOversizedBodyIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/open/third-party/users/current", func(w http.ResponseWriter, _ *http.Request) {
		jsonResponse(w, 200, `{"email":"`+strings.Repeat("x", 64)+`"}`)
	})
	cfg := testConfig(t, mux)
	if err := (Session{Config: cfg}).CacheAccessToken(Token{AccessToken: "at", ExpiresIn: 3600}); err != nil {
		t.Fatal(err)
	}
	prev := maxBodyBytes
	maxBodyBytes = 32
	t.Cleanup(func() { maxBodyBytes = prev })
	_, err := Client{Session: Session{Config: cfg}}.CurrentUser(context.Background())
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("err = %v, want ErrBodyTooLarge", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
