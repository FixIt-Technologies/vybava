package posta

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
)

// Message is one delivered mail, flattened to what a journey actually asserts
// on: who it reached, when, and the links and files it carried.
type Message struct {
	UID         uint32       `json:"uid"`
	From        string       `json:"from"`
	To          []string     `json:"to"`
	DeliveredTo string       `json:"deliveredTo,omitempty"`
	Subject     string       `json:"subject"`
	Date        time.Time    `json:"date"`
	Text        string       `json:"text,omitempty"`
	HTML        string       `json:"html,omitempty"`
	Links       []string     `json:"links"`
	Attachments []Attachment `json:"attachments"`
}

// Attachment is one file carried by a message. Data stays in memory until Save
// writes it, so `posta wait` can report what arrived without touching the disk.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	Path        string `json:"path,omitempty"`

	data []byte
}

// headers is the subset fetched in the cheap first pass, before a message has
// earned a full body download.
type headers struct {
	uid         uint32
	from        string
	to          []string
	deliveredTo string
	subject     string
	date        time.Time
}

var decoder = mime.WordDecoder{}

// parseHeaders reads the header-only fetch. A header the server mangles is
// reported as-is rather than dropped — the recipient match runs on it.
func parseHeaders(uid uint32, raw []byte) (headers, error) {
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return headers{}, fmt.Errorf("uid %d: %w", uid, err)
	}
	h := headers{
		uid:         uid,
		from:        decodeWord(parsed.Header.Get("From")),
		deliveredTo: addressOf(parsed.Header.Get("Delivered-To")),
		subject:     decodeWord(parsed.Header.Get("Subject")),
	}
	for _, field := range []string{"To", "Cc"} {
		for _, value := range strings.Split(parsed.Header.Get(field), ",") {
			if addr := addressOf(value); addr != "" {
				h.to = append(h.to, addr)
			}
		}
	}
	if date, err := parsed.Header.Date(); err == nil {
		h.date = date
	}
	return h, nil
}

// reaches reports whether this message was delivered to the exact address.
// Delivered-To is authoritative; To/Cc is the fallback for a provider that does
// not set it. The comparison is on the whole address including the +tag — that
// exactness is the entire reason parallel runs do not collide.
func (h headers) reaches(recipient string) bool {
	want := strings.ToLower(strings.TrimSpace(recipient))
	if want == "" {
		return true
	}
	if strings.EqualFold(h.deliveredTo, want) {
		return true
	}
	for _, addr := range h.to {
		if strings.EqualFold(addr, want) {
			return true
		}
	}
	return false
}

func addressOf(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := mail.ParseAddress(value); err == nil {
		return strings.ToLower(parsed.Address)
	}
	// A bare, unquoted address still has to match.
	return strings.ToLower(strings.Trim(value, "<> "))
}

func decodeWord(value string) string {
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

// parseMessage reads the full RFC822 body into the flattened form.
func parseMessage(h headers, raw []byte) (*Message, error) {
	msg := &Message{
		UID:         h.uid,
		From:        h.from,
		To:          h.to,
		DeliveredTo: h.deliveredTo,
		Subject:     h.subject,
		Date:        h.date,
		Attachments: []Attachment{},
	}
	reader, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("uid %d: %w", h.uid, err)
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("uid %d: %w", h.uid, err)
		}
		body, err := io.ReadAll(part.Body)
		if err != nil {
			return nil, fmt.Errorf("uid %d: %w", h.uid, err)
		}
		switch header := part.Header.(type) {
		case *gomail.InlineHeader:
			contentType, _, _ := header.ContentType()
			if strings.EqualFold(contentType, "text/html") {
				msg.HTML += string(body)
			} else {
				msg.Text += string(body)
			}
		case *gomail.AttachmentHeader:
			filename, _ := header.Filename()
			contentType, _, _ := header.ContentType()
			msg.Attachments = append(msg.Attachments, Attachment{
				Filename:    filename,
				ContentType: contentType,
				Size:        len(body),
				data:        body,
			})
		}
	}
	msg.Links = ExtractLinks(msg.Text, msg.HTML)
	return msg, nil
}

var (
	bareURL  = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)
	hrefAttr = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)
	// Punctuation that ends the sentence rather than the URL.
	trailingJunk = `.,;:!?)]}>"'`
)

// ExtractLinks pulls every URL out of a message, href attributes first so the
// HTML anchor a user would click outranks the same URL repeated in plain text.
// Order is preserved and duplicates are dropped, so links[0] is reliably the
// call to action in a verification or reset mail.
func ExtractLinks(text, htmlBody string) []string {
	seen := make(map[string]bool)
	links := []string{}
	add := func(raw string) {
		url := strings.TrimRight(html.UnescapeString(strings.TrimSpace(raw)), trailingJunk)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		links = append(links, url)
	}
	for _, match := range hrefAttr.FindAllStringSubmatch(htmlBody, -1) {
		if strings.HasPrefix(strings.ToLower(match[1]), "http") {
			add(match[1])
		}
	}
	for _, match := range bareURL.FindAllString(htmlBody, -1) {
		add(match)
	}
	for _, match := range bareURL.FindAllString(text, -1) {
		add(match)
	}
	return links
}

// Save writes every attachment into dir and records where each one landed.
func (m *Message) Save(dir string) error {
	if len(m.Attachments) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i := range m.Attachments {
		attachment := &m.Attachments[i]
		name := safeFilename(attachment.Filename, i)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, attachment.data, 0o644); err != nil {
			return err
		}
		attachment.Path = path
	}
	return nil
}

// safeFilename keeps a hostile or absent attachment name from escaping dir.
func safeFilename(name string, index int) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) || name == ".." {
		return fmt.Sprintf("attachment-%d", index+1)
	}
	return name
}
