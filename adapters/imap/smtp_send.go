package imap

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// smtpSender opens one SMTP connection and exposes a Send per message.
// Close shuts the connection down. A sender is single-use across one
// Commit invocation — spec §4 says "discovery runs once per invocation,
// synchronously, before any SMTP send starts".
type smtpSender struct {
	client *smtp.Client
	host   string
}

// smtpAuthError is the sentinel the caller uses to distinguish an
// authentication failure (prompt-for-password branch) from a transport
// failure (surface as service error).
type smtpAuthError struct{ err error }

func (e *smtpAuthError) Error() string { return "smtp auth failed: " + e.err.Error() }
func (e *smtpAuthError) Unwrap() error { return e.err }

// dialSMTP opens the connection per endpoint mode and runs AUTH PLAIN. On
// AUTH failure, returns an *smtpAuthError so callers can switch to the
// prompt-for-password branch without re-establishing the connection state.
func dialSMTP(
	ctx context.Context, ep smtpEndpoint, user, password string,
) (*smtpSender, error) {
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", ep.addr())
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var client *smtp.Client
	if ep.Mode == "tls" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: ep.Host})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		client, err = smtp.NewClient(tlsConn, ep.Host)
	} else {
		client, err = smtp.NewClient(conn, ep.Host)
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); !ok {
				_ = client.Quit()
				return nil, fmt.Errorf(
					"%s: server does not advertise STARTTLS", ep.addr())
			}
			if err = client.StartTLS(&tls.Config{ServerName: ep.Host}); err != nil {
				_ = client.Quit()
				return nil, err
			}
		}
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	if err := client.Auth(smtp.PlainAuth("", user, password, ep.Host)); err != nil {
		_ = client.Quit()
		return nil, &smtpAuthError{err: err}
	}
	return &smtpSender{client: client, host: ep.Host}, nil
}

func (s *smtpSender) Close() {
	_ = s.client.Quit()
}

// Send submits one RFC 5322 message. from is the envelope sender; to is
// the union of To/Cc/Bcc addresses (already decoded).
func (s *smtpSender) Send(from string, to []string, msg []byte) error {
	if err := s.client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := s.client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := s.client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// parsedMessage is the bits of an .eml the adapter needs to drive the
// envelope + write the sent/ path.
type parsedMessage struct {
	From       string
	Recipients []string // To + Cc + Bcc, deduped, for envelope
	Subject    string
	MessageID  string
	Date       time.Time
}

// parseOutgoingMessage reads the headers of a draft .eml. Missing From
// or recipients produce an error — the adapter rejects such drafts
// rather than guessing.
func parseOutgoingMessage(body []byte) (parsedMessage, error) {
	m, err := mail.ReadMessage(bytes.NewReader(body))
	if err != nil {
		return parsedMessage{}, fmt.Errorf("parse message: %w", err)
	}
	h := m.Header

	from, err := firstAddress(h.Get("From"))
	if err != nil || from == "" {
		return parsedMessage{}, errors.New(
			"draft is missing a valid From: header")
	}

	rcpts, err := allAddresses(h.Get("To"), h.Get("Cc"), h.Get("Bcc"))
	if err != nil {
		return parsedMessage{}, err
	}
	if len(rcpts) == 0 {
		return parsedMessage{}, errors.New(
			"draft has no To:, Cc:, or Bcc: recipient")
	}

	date, _ := mail.ParseDate(h.Get("Date"))
	msgID := strings.TrimSpace(h.Get("Message-ID"))

	subject := h.Get("Subject")

	return parsedMessage{
		From:       from,
		Recipients: rcpts,
		Subject:    subject,
		MessageID:  msgID,
		Date:       date,
	}, nil
}

func firstAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil || len(addrs) == 0 {
		return "", err
	}
	return addrs[0].Address, nil
}

func allAddresses(headers ...string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range headers {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		addrs, err := mail.ParseAddressList(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid address header %q: %w", raw, err)
		}
		for _, a := range addrs {
			if _, dup := seen[a.Address]; dup {
				continue
			}
			seen[a.Address] = struct{}{}
			out = append(out, a.Address)
		}
	}
	return out, nil
}

// applyMessageDefaults fills Subject from -m when the draft left it blank
// (commit design §1: "email overrides Subject when To: is set and
// Subject: missing"). Returns the possibly-modified bytes and the
// final Subject.
func applyMessageDefaults(raw []byte, commitMessage string) ([]byte, string, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	existing := strings.TrimSpace(m.Header.Get("Subject"))
	if existing != "" || commitMessage == "" {
		return raw, existing, nil
	}
	// Inject Subject: <commitMessage> as the first header line.
	patched := append(
		[]byte("Subject: "+commitMessage+"\r\n"),
		raw...,
	)
	return patched, commitMessage, nil
}

// ensureMessageID stamps a Message-ID if the draft omitted it, so the
// sent/ filename is stable. Returns the bytes with the header present
// and the final ID.
func ensureMessageID(raw []byte, existingID, host string, now time.Time) ([]byte, string) {
	if existingID != "" {
		return raw, existingID
	}
	id := fmt.Sprintf("<%d.%x@%s>", now.UnixNano(), now.UnixNano()&0xFFFF, host)
	patched := append(
		[]byte("Message-ID: "+id+"\r\n"),
		raw...,
	)
	return patched, id
}
