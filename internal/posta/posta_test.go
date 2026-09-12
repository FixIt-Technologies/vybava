package posta

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAddressTagsTheMailboxPerRun(t *testing.T) {
	got, err := Address("someone@example.com", "FixIt", "Customer", "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "someone+fixit-customer-a1b2@example.com"; got != want {
		t.Fatalf("address = %q, want %q", got, want)
	}

	// An already-tagged base must not nest: someone+a+b@ is a different,
	// unroutable address on some providers.
	got, err = Address("someone+old@example.com", "fixit", "", "run9")
	if err != nil {
		t.Fatal(err)
	}
	if want := "someone+fixit-run9@example.com"; got != want {
		t.Fatalf("re-tagged address = %q, want %q", got, want)
	}

	if _, err := Address("not-an-address", "fixit", "", "r"); err == nil {
		t.Fatal("a base without a domain must be rejected")
	}
	if _, err := Address("someone@example.com", "!!!", "", ""); err == nil {
		t.Fatal("a tag that slugs to nothing must be rejected")
	}
}

func TestSlugKeepsOnlyRoutableCharacters(t *testing.T) {
	for input, want := range map[string]string{
		"FixIt":             "fixit",
		"  password reset ": "password-reset",
		"a//b":              "a-b",
		"Příliš":            "p-li",
		"!!!":               "",
	} {
		if got := Slug(input); got != want {
			t.Fatalf("Slug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRunIDIsUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := RunID()
		if seen[id] {
			t.Fatalf("RunID repeated %q — two parallel runs would share an address", id)
		}
		seen[id] = true
	}
}

// The exact-recipient match is what keeps two agents running the same journey
// from reading each other's reset links.
func TestReachesMatchesTheWholeTaggedAddress(t *testing.T) {
	h := headers{
		deliveredTo: "someone+fixit-customer-a1b2@example.com",
		to:          []string{"someone+fixit-customer-a1b2@example.com"},
	}
	if !h.reaches("someone+fixit-customer-a1b2@example.com") {
		t.Fatal("the delivered address must match")
	}
	if !h.reaches("SOMEONE+FixIt-Customer-A1B2@example.com") {
		t.Fatal("matching must ignore case")
	}
	if h.reaches("someone+fixit-customer-zzzz@example.com") {
		t.Fatal("a different run's address must not match")
	}
	if h.reaches("someone@example.com") {
		t.Fatal("the untagged mailbox address must not match a tagged delivery")
	}

	// Delivered-To absent: To/Cc is the fallback.
	fallback := headers{to: []string{"other@example.com", "someone+x-y@example.com"}}
	if !fallback.reaches("someone+x-y@example.com") {
		t.Fatal("To must be used when Delivered-To is absent")
	}
}

func TestExtractLinksPrefersAnchorsAndDedupes(t *testing.T) {
	text := "Reset here: https://app.example.com/reset?token=abc123.\nIgnore https://app.example.com/reset?token=abc123 twice."
	html := `<p>Click <a href="https://app.example.com/reset?token=abc123&amp;lang=cs">here</a> or visit https://example.com/help</p>`

	links := ExtractLinks(text, html)
	if len(links) != 3 {
		t.Fatalf("links = %#v, want 3 unique", links)
	}
	// The anchor a user would actually click comes first, entity-decoded.
	if want := "https://app.example.com/reset?token=abc123&lang=cs"; links[0] != want {
		t.Fatalf("links[0] = %q, want %q", links[0], want)
	}
	// The sentence-ending period is not part of the URL.
	for _, link := range links {
		if strings.HasSuffix(link, ".") {
			t.Fatalf("trailing punctuation kept in %q", link)
		}
	}
}

func TestParseHeadersDecodesAndNormalises(t *testing.T) {
	raw := "From: =?utf-8?q?Luk=C3=A1=C5=A1?= <noreply@app.example.com>\r\n" +
		"To: Someone <SOMEONE+fixit-a1b2@example.com>\r\n" +
		"Delivered-To: someone+fixit-a1b2@example.com\r\n" +
		"Subject: =?utf-8?q?Obnoven=C3=AD_hesla?=\r\n" +
		"Date: Thu, 11 Sep 2026 18:03:00 +0200\r\n\r\n"

	h, err := parseHeaders(7, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if h.subject != "Obnovení hesla" {
		t.Fatalf("subject = %q, want the decoded word", h.subject)
	}
	if !strings.Contains(h.from, "Lukáš") {
		t.Fatalf("from = %q, want the decoded display name", h.from)
	}
	if h.deliveredTo != "someone+fixit-a1b2@example.com" {
		t.Fatalf("deliveredTo = %q", h.deliveredTo)
	}
	if h.date.IsZero() {
		t.Fatal("Date must be parsed")
	}
}

// InternalDate, not the sender's Date:, decides the cutoff — a sender with a
// wrong clock must not be able to hide its own mail from a waiting journey.
func TestQueryMatchesOnDeliveryTime(t *testing.T) {
	since := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC)
	h := headers{
		deliveredTo: "someone+x@example.com",
		subject:     "Reset your password",
		date:        since.Add(-72 * time.Hour), // sender's clock is wrong
	}
	query := Query{Recipient: "someone+x@example.com", Since: since}

	if !query.matches(h, since.Add(time.Second)) {
		t.Fatal("a mail delivered after the cutoff must match despite a stale Date:")
	}
	if query.matches(h, since.Add(-time.Second)) {
		t.Fatal("a mail delivered before the cutoff must not match")
	}
}

func TestSafeFilenameCannotEscapeTheOutputDirectory(t *testing.T) {
	for input, want := range map[string]string{
		"../../etc/passwd": "passwd",
		"/etc/passwd":      "passwd",
		"":                 "attachment-1",
		"..":               "attachment-1",
		"invoice.pdf":      "invoice.pdf",
	} {
		if got := safeFilename(input, 0); got != want {
			t.Fatalf("safeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

// The mailbox is a real account. Purge must refuse anything but an address
// posta itself minted, before it ever reaches the network — hence the nil
// client here: a guard that needed a connection would already be too late.
func TestPurgeRefusesAnUntaggedRecipient(t *testing.T) {
	box := &Mailbox{}
	for _, recipient := range []string{"", "someone@example.com", "not-an-address", "someone"} {
		deleted, err := box.Purge(Query{Recipient: recipient})
		if !errors.Is(err, ErrUntaggedPurge) {
			t.Fatalf("Purge(%q) error = %v, want ErrUntaggedPurge", recipient, err)
		}
		if deleted != 0 {
			t.Fatalf("Purge(%q) deleted %d", recipient, deleted)
		}
	}
}

func TestCredentialsFromEnvRefusesAMissingPassword(t *testing.T) {
	t.Setenv(EnvAddress, "someone@example.com")
	t.Setenv(EnvPassword, "")
	if _, err := CredentialsFromEnv(); err == nil {
		t.Fatal("a missing password must be an error, never a prompt or a fallback")
	}

	t.Setenv(EnvPassword, "injected-by-the-vault")
	creds, err := CredentialsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if creds.IMAPAddr != DefaultIMAPAddr || creds.SMTPAddr != DefaultSMTPAddr {
		t.Fatalf("endpoints = %q / %q, want the Gmail defaults", creds.IMAPAddr, creds.SMTPAddr)
	}
}
