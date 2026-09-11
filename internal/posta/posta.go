// Package posta drives the shared AI test mailbox — the one inbox an agent may
// read end to end. It mints a per-run delivery address, waits for the message a
// journey is expecting, and pulls the links and attachments back out, so a
// signup-verification or password-reset flow can be driven without a human.
//
// The mailbox password is never a literal in this package: it arrives in
// POSTA_APP_PASSWORD, injected by the vault at the point of use, and is never
// printed, logged or returned. Errors quote the mailbox address, never the
// credential.
package posta

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// EnvAddress holds the mailbox's own address, e.g. someone@gmail.com.
	EnvAddress = "POSTA_ADDRESS"
	// EnvPassword holds the app password, injected by the vault.
	EnvPassword = "POSTA_APP_PASSWORD"
	// EnvIMAP and EnvSMTP override the endpoints for a non-Gmail mailbox.
	EnvIMAP = "POSTA_IMAP_ADDR"
	EnvSMTP = "POSTA_SMTP_ADDR"

	DefaultIMAPAddr = "imap.gmail.com:993"
	DefaultSMTPAddr = "smtp.gmail.com:465"
)

// Credentials name the mailbox and how to reach it.
type Credentials struct {
	Address  string
	Password string
	IMAPAddr string
	SMTPAddr string
}

// CredentialsFromEnv reads the mailbox identity the vault injected. A missing
// password is an error rather than a prompt: posta is meant to run unattended
// behind `onyx run_command --env-refs`, and a fallback would be a way to run it
// against the wrong mailbox.
func CredentialsFromEnv() (Credentials, error) {
	creds := Credentials{
		Address:  strings.TrimSpace(os.Getenv(EnvAddress)),
		Password: os.Getenv(EnvPassword),
		IMAPAddr: envOr(EnvIMAP, DefaultIMAPAddr),
		SMTPAddr: envOr(EnvSMTP, DefaultSMTPAddr),
	}
	if creds.Address == "" {
		return Credentials{}, fmt.Errorf("%s is empty: inject the mailbox address", EnvAddress)
	}
	if !strings.Contains(creds.Address, "@") {
		return Credentials{}, fmt.Errorf("%s is not an email address: %q", EnvAddress, creds.Address)
	}
	if creds.Password == "" {
		return Credentials{}, fmt.Errorf("%s is empty: inject the app password with the vault, never a literal", EnvPassword)
	}
	return creds, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// ErrEmptyTag reports an address tag that slugged away to nothing.
var ErrEmptyTag = errors.New("project, role and run slugged to an empty tag")

// Address derives the delivery address a single test run owns. Gmail routes
// every local+tag form back to the same mailbox, so a run holds an address
// nobody else reads without anything being provisioned first. The application
// under test must keep the whole address when it identifies the user — stripping
// the +tag collapses every run back into one account.
func Address(base, project, role, run string) (string, error) {
	local, domain, found := strings.Cut(strings.TrimSpace(base), "@")
	if !found || local == "" || domain == "" {
		return "", fmt.Errorf("mailbox address %q is not local@domain", base)
	}
	// A base that already carries a tag would otherwise nest: a+b+c@…
	local, _, _ = strings.Cut(local, "+")
	tag := Tag(project, role, run)
	if tag == "" {
		return "", ErrEmptyTag
	}
	return local + "+" + tag + "@" + domain, nil
}

// Tag joins the address parts into one slug, skipping the empty ones.
func Tag(parts ...string) string {
	slugged := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := Slug(part); s != "" {
			slugged = append(slugged, s)
		}
	}
	return strings.Join(slugged, "-")
}

// Slug reduces one address part to the characters a mail local part survives
// intact. Anything else becomes a separator, because a tag that the provider
// rewrites is a tag the journey can no longer match on.
func Slug(part string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(part)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// RunID mints the short random tag that keeps two concurrent runs of the same
// journey from reading each other's mail.
func RunID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("posta: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}
