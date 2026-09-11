package posta

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// DefaultMailbox is where a journey looks for its mail.
//
// All Mail rather than INBOX, because INBOX is a label and a label can be
// missed: Gmail keeps a self-addressed message out of the inbox entirely, files
// bulk senders under Promotions, and honours any filter the account carries. A
// journey that waited on INBOX would report "the mail never arrived" for mail
// that arrived perfectly well. All Mail holds every delivered message.
//
// The trade-off is that All Mail also holds the mailbox's own sent copies, so a
// test that must prove inbox delivery specifically should pass
// --mailbox INBOX and accept the label risk knowingly.
const DefaultMailbox = "[Gmail]/All Mail"

// Query selects the messages one journey step is waiting for.
//
// Recipient is the exact plus-address. Matching on it — rather than on "the
// newest mail in the inbox" — is what stops two agents running the same journey
// from stealing each other's reset links.
type Query struct {
	Recipient string
	Subject   *regexp.Regexp
	Since     time.Time
	Mailbox   string
}

func (q Query) mailbox() string {
	if q.Mailbox == "" {
		return DefaultMailbox
	}
	return q.Mailbox
}

// Mailbox is a live IMAP connection to the test mailbox.
type Mailbox struct {
	client *imapclient.Client
	creds  Credentials
}

// Dial opens and authenticates the connection. The password is handed straight
// to LOGIN and never retained by this package.
func Dial(creds Credentials) (*Mailbox, error) {
	client, err := imapclient.DialTLS(creds.IMAPAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", creds.IMAPAddr, err)
	}
	if err := client.Login(creds.Address, creds.Password).Wait(); err != nil {
		_ = client.Close()
		// The server's rejection text is safe; the credential is not in it.
		return nil, fmt.Errorf("login %s: %w", creds.Address, err)
	}
	return &Mailbox{client: client, creds: creds}, nil
}

// Close logs out, ignoring a server that has already dropped the connection.
func (m *Mailbox) Close() error {
	if err := m.client.Logout().Wait(); err != nil {
		return m.client.Close()
	}
	return m.client.Close()
}

// header-only fetch: enough to decide whether a message is ours, without paying
// for bodies and attachments we are about to discard.
var headerFields = []string{"From", "To", "Cc", "Subject", "Date", "Delivered-To", "Message-Id"}

// Search returns every message matching the query, newest first.
//
// The server is only asked to narrow by date, because SINCE is the one
// criterion every provider implements identically. Recipient and subject are
// then matched here, against the real headers — a server-side header search
// that silently under-matches would look exactly like "the mail never arrived".
func (m *Mailbox) Search(query Query) ([]*Message, error) {
	if _, err := m.client.Select(query.mailbox(), nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", query.mailbox(), err)
	}
	criteria := &imap.SearchCriteria{}
	if !query.Since.IsZero() {
		// IMAP SINCE has day granularity and compares in the server's timezone;
		// widening by a day keeps a run started near midnight from missing its
		// own mail. The exact cutoff is re-applied below.
		criteria.Since = query.Since.AddDate(0, 0, -1)
	}
	found, err := m.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", query.mailbox(), err)
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}

	// Specifier must be HEADER: the server echoes the section back as
	// BODY[HEADER.FIELDS (...)], and the response is matched on it. Leaving it
	// unset makes every fetched message silently unreadable.
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: headerFields,
		Peek:         true,
	}
	buffers, err := m.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:          true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch headers: %w", err)
	}

	var matched []headers
	var readable int
	for _, buffer := range buffers {
		raw := buffer.FindBodySection(section)
		if raw == nil {
			continue
		}
		readable++
		head, err := parseHeaders(uint32(buffer.UID), raw)
		if err != nil {
			return nil, err
		}
		if head.date.IsZero() {
			head.date = buffer.InternalDate
		}
		if !query.matches(head, buffer.InternalDate) {
			continue
		}
		matched = append(matched, head)
	}
	// A server that answered with sections we cannot read looks exactly like an
	// empty mailbox, and a journey would wait out its whole timeout on it. Say so
	// instead.
	if readable == 0 {
		return nil, fmt.Errorf("fetched %d message(s) from %s but could not read a header section from any of them", len(buffers), query.mailbox())
	}
	if len(matched) == 0 {
		return nil, nil
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].date.After(matched[j].date) })
	return m.fetchBodies(matched)
}

// matches applies the criteria the server was not asked about. InternalDate is
// the delivery time and is the honest cutoff — Date: is written by the sender
// and a badly-clocked sender would otherwise hide its own mail.
func (q Query) matches(head headers, delivered time.Time) bool {
	if !head.reaches(q.Recipient) {
		return false
	}
	if q.Subject != nil && !q.Subject.MatchString(head.subject) {
		return false
	}
	if !q.Since.IsZero() && delivered.Before(q.Since) {
		return false
	}
	return true
}

func (m *Mailbox) fetchBodies(matched []headers) ([]*Message, error) {
	uids := make([]imap.UID, 0, len(matched))
	for _, head := range matched {
		uids = append(uids, imap.UID(head.uid))
	}
	full := &imap.FetchItemBodySection{Peek: true}
	buffers, err := m.client.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{full},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch bodies: %w", err)
	}
	byUID := make(map[uint32][]byte, len(buffers))
	for _, buffer := range buffers {
		if raw := buffer.FindBodySection(full); raw != nil {
			byUID[uint32(buffer.UID)] = raw
		}
	}
	// Rebuild in the order matched already established, not the server's.
	messages := make([]*Message, 0, len(matched))
	for _, head := range matched {
		raw, ok := byUID[head.uid]
		if !ok {
			continue
		}
		message, err := parseMessage(head, raw)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// ErrTimeout reports that the awaited message never arrived.
var ErrTimeout = fmt.Errorf("no matching message arrived")

// Wait polls until a message matches, and returns the newest one. Newest rather
// than first: when a journey requests two reset mails, only the latest token is
// still valid.
func (m *Mailbox) Wait(ctx context.Context, query Query, interval time.Duration) (*Message, error) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		messages, err := m.Search(query)
		if err != nil {
			return nil, err
		}
		if len(messages) > 0 {
			return messages[0], nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w for %s", ErrTimeout, query.Recipient)
		case <-ticker.C:
		}
	}
}

// ErrUntaggedPurge reports a purge aimed at something other than a test address.
var ErrUntaggedPurge = fmt.Errorf("purge needs a +tagged recipient")

// Purge deletes every message matching the query and reports how many went.
// Between runs this is what stops a stale reset link from satisfying the next
// run's wait.
//
// It refuses any recipient without a +tag. The mailbox is a real account with
// real mail in it, and a tagged address is one posta minted for a test run —
// so this is structurally unable to delete anything a human cares about, no
// matter what the caller passes.
func (m *Mailbox) Purge(query Query) (int, error) {
	local, _, found := strings.Cut(query.Recipient, "@")
	if !found || !strings.Contains(local, "+") {
		return 0, fmt.Errorf("%w, got %q", ErrUntaggedPurge, query.Recipient)
	}
	messages, err := m.Search(query)
	if err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return 0, nil
	}
	uids := make([]imap.UID, 0, len(messages))
	for _, message := range messages {
		uids = append(uids, imap.UID(message.UID))
	}
	store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}, Silent: true}
	if err := m.client.Store(imap.UIDSetNum(uids...), store, nil).Close(); err != nil {
		return 0, fmt.Errorf("flag deleted: %w", err)
	}
	if err := m.client.Expunge().Close(); err != nil {
		return 0, fmt.Errorf("expunge: %w", err)
	}
	return len(messages), nil
}
