package posta

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"time"
)

// Outgoing is one mail posta sends — the half of a journey where the mailbox is
// the sender rather than the recipient (replying to a thread, seeding a form).
type Outgoing struct {
	To      []string
	Subject string
	Body    string
	Attach  []string
}

// Send delivers the mail over implicit TLS. The app password authenticates the
// session and is never written to the message or to any log.
func Send(creds Credentials, out Outgoing) error {
	if len(out.To) == 0 {
		return fmt.Errorf("no recipient")
	}
	body, err := compose(creds.Address, out)
	if err != nil {
		return err
	}
	client, err := dialSMTP(creds)
	if err != nil {
		return err
	}
	defer func() { _ = client.Quit() }()

	if err := client.Mail(creds.Address); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, recipient := range out.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("rcpt %s: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return writer.Close()
}

// dialSMTP opens an authenticated session over implicit TLS.
func dialSMTP(creds Credentials) (*smtp.Client, error) {
	host, _, err := net.SplitHostPort(creds.SMTPAddr)
	if err != nil {
		return nil, fmt.Errorf("smtp address %q: %w", creds.SMTPAddr, err)
	}
	conn, err := tls.Dial("tcp", creds.SMTPAddr, &tls.Config{ServerName: host})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", creds.SMTPAddr, err)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp %s: %w", creds.SMTPAddr, err)
	}
	if err := client.Auth(smtp.PlainAuth("", creds.Address, creds.Password, host)); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("auth %s: %w", creds.Address, err)
	}
	return client, nil
}

// CheckSMTP proves the mailbox can send without sending anything, so `doctor`
// stays safe to run against a real account.
func CheckSMTP(creds Credentials) error {
	client, err := dialSMTP(creds)
	if err != nil {
		return err
	}
	return client.Quit()
}

// compose builds the RFC 5322 message: plain text alone, or multipart/mixed
// once there is a file to carry.
func compose(from string, out Outgoing) ([]byte, error) {
	var buf bytes.Buffer
	header := func(key, value string) {
		fmt.Fprintf(&buf, "%s: %s\r\n", key, value)
	}
	header("From", from)
	for _, recipient := range out.To {
		header("To", recipient)
	}
	header("Subject", mime.QEncoding.Encode("utf-8", out.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")

	if len(out.Attach) == 0 {
		header("Content-Type", `text/plain; charset="utf-8"`)
		buf.WriteString("\r\n")
		buf.WriteString(out.Body)
		return buf.Bytes(), nil
	}

	writer := multipart.NewWriter(&buf)
	header("Content-Type", `multipart/mixed; boundary="`+writer.Boundary()+`"`)
	buf.WriteString("\r\n")

	text, err := writer.CreatePart(map[string][]string{
		"Content-Type": {`text/plain; charset="utf-8"`},
	})
	if err != nil {
		return nil, err
	}
	if _, err := text.Write([]byte(out.Body)); err != nil {
		return nil, err
	}
	for _, path := range out.Attach {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("attach %s: %w", path, err)
		}
		name := filepath.Base(path)
		part, err := writer.CreatePart(map[string][]string{
			"Content-Type":              {"application/octet-stream"},
			"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", name)},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(part, content); err != nil {
			return nil, fmt.Errorf("attach %s: %w", path, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeBase64 emits the payload in 76-character lines, the limit RFC 2045 sets
// and some servers still enforce by rejecting the message.
func writeBase64(w io.Writer, content []byte) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	for len(encoded) > 0 {
		line := encoded
		if len(line) > 76 {
			line = line[:76]
		}
		encoded = encoded[len(line):]
		if _, err := io.WriteString(w, line+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}
