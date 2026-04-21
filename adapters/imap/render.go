package imap

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	htm "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/KrzysztofBogdan/gitfs/adapter"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"gopkg.in/yaml.v3"
)

// renderMessage turns a fetched IMAP message into one markdown file. The
// caller assigns filename and mtime in a planning pass so same-subject
// messages can be numbered (§3.3) and mtime can track the email's date
// (§4). filename is the basename (e.g. "Re- ping.md"); logical is the
// enclosing folder (e.g. "inbox").
func renderMessage(
	emit adapter.Emitter, logical, filename string,
	mtime time.Time, buf *imapclient.FetchMessageBuffer,
) error {
	env := buf.Envelope
	var rawBody []byte
	for _, seg := range buf.BodySection {
		if len(seg.Bytes) > 0 {
			rawBody = seg.Bytes
			break
		}
	}

	fm := frontmatter{
		From:      addrList(env.From),
		To:        addrListSlice(env.To),
		Cc:        addrListSlice(env.Cc),
		Bcc:       addrListSlice(env.Bcc),
		Subject:   env.Subject,
		MessageID: bracket(env.MessageID),
		InReplyTo: firstBracketed(env.InReplyTo),
		Date:      formatDate(env.Date, buf.InternalDate),
	}

	body, attachments := extractBody(rawBody)
	fm.Attachments = attachments

	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(fmBytes)
	out.WriteString("---\n\n")
	out.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		out.WriteByte('\n')
	}

	return emit.FileAt(logical+"/"+filename, out.Bytes(), mtime)
}

type frontmatter struct {
	From        string    `yaml:"from"`
	To          []string  `yaml:"to"`
	Cc          []string  `yaml:"cc,omitempty"`
	Bcc         []string  `yaml:"bcc,omitempty"`
	Date        string    `yaml:"date"`
	Subject     string    `yaml:"subject"`
	MessageID   string    `yaml:"message_id"`
	InReplyTo   string    `yaml:"in_reply_to,omitempty"`
	Attachments []attInfo `yaml:"attachments,omitempty"`
}

type attInfo struct {
	Name string `yaml:"name"`
	Size int64  `yaml:"size"`
}

func addrList(addrs []goimap.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, formatAddr(a))
	}
	return strings.Join(parts, ", ")
}

// addrListSlice mirrors addrList but returns a slice for yaml lists.
// Unused for From (which stays a single string) — used for To/Cc/Bcc.
func addrListSlice(addrs []goimap.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, formatAddr(a))
	}
	return out
}

func formatAddr(a goimap.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s@%s>", a.Name, a.Mailbox, a.Host)
	}
	return fmt.Sprintf("%s@%s", a.Mailbox, a.Host)
}

func bracket(msgID string) string {
	if msgID == "" {
		return ""
	}
	if strings.HasPrefix(msgID, "<") {
		return msgID
	}
	return "<" + msgID + ">"
}

func firstBracketed(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return bracket(ids[0])
}

func formatDate(t time.Time, fallback time.Time) string {
	if t.IsZero() {
		t = fallback
	}
	return t.UTC().Format(time.RFC3339)
}

// sanitizeSubject turns a decoded subject into a filesystem-safe basename
// per spec §3.2. The returned value is never empty; inputs that collapse
// to nothing yield "no-subject".
func sanitizeSubject(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isForbiddenRune(r) {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	out := collapseDashes(b.String())
	out = trimEdges(out)
	out = truncateUTF8(out, 100)
	out = trimEdges(out)
	if out == "" {
		return "no-subject"
	}
	return out
}

func isForbiddenRune(r rune) bool {
	switch r {
	case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
		return true
	}
	if r < 0x20 || r == 0x7F {
		return true
	}
	return false
}

func collapseDashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if !prevDash {
				b.WriteByte('-')
			}
			prevDash = true
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	return b.String()
}

func trimEdges(s string) string {
	return strings.Trim(s, "-. \t")
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// extractBody walks the raw RFC 5322 message and returns the best body
// text plus attachment info. Prefers text/plain; falls back to markdown
// converted from text/html.
func extractBody(raw []byte) (string, []attInfo) {
	if len(raw) == 0 {
		return "", nil
	}
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return string(raw), nil
	}
	defer mr.Close()

	var plain, html string
	var atts []attInfo
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			b, _ := io.ReadAll(part.Body)
			if ct == "text/html" && html == "" {
				html = string(b)
			} else if (ct == "" || ct == "text/plain") && plain == "" {
				plain = string(b)
			}
		case *mail.AttachmentHeader:
			name, _ := h.Filename()
			if name == "" {
				if ct := h.Get("Content-Type"); ct != "" {
					if _, p, err := mime.ParseMediaType(ct); err == nil {
						if n, ok := p["name"]; ok {
							name = n
						}
					}
				}
			}
			b, _ := io.ReadAll(part.Body)
			atts = append(atts, attInfo{Name: name, Size: int64(len(b))})
		}
	}

	if plain != "" {
		return plain, atts
	}
	if html != "" {
		if md, err := htm.ConvertString(html); err == nil {
			return md, atts
		}
		return html, atts
	}
	return string(raw), atts
}
