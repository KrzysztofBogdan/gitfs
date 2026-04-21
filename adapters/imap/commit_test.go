package imap

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStaticTableCandidatesGmail(t *testing.T) {
	got := staticTableCandidates("imap.gmail.com")
	if len(got) != 1 || got[0].Host != "smtp.gmail.com" || got[0].Port != 587 {
		t.Errorf("gmail candidates = %+v", got)
	}
}

func TestStaticTableCandidatesUnknown(t *testing.T) {
	got := staticTableCandidates("imap.example.com")
	if len(got) != 0 {
		t.Errorf("unknown host: got %+v, want none", got)
	}
}

func TestHostSubstitutionCandidatesImapPrefix(t *testing.T) {
	got := hostSubstitutionCandidates("imap.acme.org")
	if len(got) != 2 ||
		got[0].Host != "smtp.acme.org" || got[0].Port != 587 || got[0].Mode != "starttls" ||
		got[1].Host != "smtp.acme.org" || got[1].Port != 465 || got[1].Mode != "tls" {
		t.Errorf("candidates = %+v", got)
	}
}

func TestHostSubstitutionCandidatesMailPrefix(t *testing.T) {
	got := hostSubstitutionCandidates("mail.acme.org")
	if len(got) != 2 || got[0].Host != "smtp.acme.org" {
		t.Errorf("candidates = %+v", got)
	}
}

func TestHostSubstitutionCandidatesNoPrefix(t *testing.T) {
	got := hostSubstitutionCandidates("acme.org")
	if got != nil {
		t.Errorf("no-prefix host should yield no candidates, got %+v", got)
	}
}

func TestParseAutoconfig(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<clientConfig version="1.1"><emailProvider>
  <outgoingServer type="smtp">
    <hostname>smtp.example.com</hostname>
    <port>587</port>
    <socketType>STARTTLS</socketType>
  </outgoingServer>
</emailProvider></clientConfig>`)
	ep, ok := parseAutoconfig(body)
	if !ok || ep.Host != "smtp.example.com" || ep.Port != 587 || ep.Mode != "starttls" {
		t.Errorf("ep = %+v, ok = %v", ep, ok)
	}
}

func TestParseAutoconfigSSL(t *testing.T) {
	body := []byte(`<clientConfig><emailProvider>
  <outgoingServer type="smtp">
    <hostname>smtp.x.com</hostname><port>465</port><socketType>SSL</socketType>
  </outgoingServer>
</emailProvider></clientConfig>`)
	ep, ok := parseAutoconfig(body)
	if !ok || ep.Mode != "tls" {
		t.Errorf("ep = %+v", ep)
	}
}

func TestParseAutoconfigRejectsNonSMTP(t *testing.T) {
	body := []byte(`<clientConfig><emailProvider>
  <outgoingServer type="imap">
    <hostname>imap.x.com</hostname><port>993</port>
  </outgoingServer>
</emailProvider></clientConfig>`)
	_, ok := parseAutoconfig(body)
	if ok {
		t.Error("imap outgoingServer should not be accepted")
	}
}

func TestDiscoverSMTPStaticHit(t *testing.T) {
	dd := discoveryDeps{
		TotalBudget:   time.Second,
		PerMechBudget: time.Second,
		Handshake: func(_ context.Context, ep smtpEndpoint) error {
			if ep.Host == "smtp.gmail.com" {
				return nil
			}
			return errors.New("nope")
		},
	}
	ep, ok, err := discoverSMTP(context.Background(), "imap.gmail.com:993", "me@gmail.com", dd)
	if err != nil || !ok {
		t.Fatalf("discover: ok=%v err=%v", ok, err)
	}
	if ep.Host != "smtp.gmail.com" || ep.Port != 587 {
		t.Errorf("ep = %+v", ep)
	}
}

func TestDiscoverSMTPFallsBackToSubstitution(t *testing.T) {
	dd := discoveryDeps{
		TotalBudget:   time.Second,
		PerMechBudget: time.Second,
		Handshake: func(_ context.Context, ep smtpEndpoint) error {
			if ep.Host == "smtp.acme.org" && ep.Port == 587 {
				return nil
			}
			return errors.New("nope")
		},
	}
	ep, ok, _ := discoverSMTP(context.Background(),
		"imap.acme.org:993", "me@acme.org", dd)
	if !ok || ep.Host != "smtp.acme.org" || ep.Port != 587 {
		t.Errorf("ep = %+v, ok = %v", ep, ok)
	}
}

func TestDiscoverSMTPFallsBackToSRV(t *testing.T) {
	calls := 0
	dd := discoveryDeps{
		TotalBudget:   time.Second,
		PerMechBudget: time.Second,
		Handshake: func(_ context.Context, ep smtpEndpoint) error {
			calls++
			if ep.Host == "submission.acme.org" {
				return nil
			}
			return errors.New("nope")
		},
		LookupSRV: func(_, _, _ string) (string, []*net.SRV, error) {
			return "", []*net.SRV{
				{Target: "submission.acme.org.", Port: 587, Priority: 10},
			}, nil
		},
	}
	ep, ok, _ := discoverSMTP(context.Background(),
		"anything.unknown:993", "me@acme.org", dd)
	if !ok || ep.Host != "submission.acme.org" || ep.Port != 587 {
		t.Errorf("ep = %+v, ok = %v", ep, ok)
	}
	if calls < 1 {
		t.Errorf("handshake never called")
	}
}

func TestDiscoverSMTPFallsBackToAutoconfig(t *testing.T) {
	xml := `<clientConfig><emailProvider><outgoingServer type="smtp">
	<hostname>mta.acme.org</hostname><port>587</port><socketType>STARTTLS</socketType>
	</outgoingServer></emailProvider></clientConfig>`
	dd := discoveryDeps{
		TotalBudget:   time.Second,
		PerMechBudget: time.Second,
		Handshake: func(_ context.Context, ep smtpEndpoint) error {
			if ep.Host == "mta.acme.org" {
				return nil
			}
			return errors.New("nope")
		},
		LookupSRV: func(_, _, _ string) (string, []*net.SRV, error) {
			return "", nil, errors.New("no record")
		},
		HTTPGet: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(xml), nil
		},
		AutoconfigURLs: func(domain string) []string {
			return []string{"https://stub/" + domain}
		},
	}
	ep, ok, _ := discoverSMTP(context.Background(),
		"anything.unknown:993", "me@acme.org", dd)
	if !ok || ep.Host != "mta.acme.org" {
		t.Errorf("ep = %+v, ok = %v", ep, ok)
	}
}

func TestDiscoverSMTPAllFail(t *testing.T) {
	dd := discoveryDeps{
		TotalBudget:   time.Second,
		PerMechBudget: time.Second,
		Handshake: func(_ context.Context, _ smtpEndpoint) error {
			return errors.New("refused")
		},
		LookupSRV: func(_, _, _ string) (string, []*net.SRV, error) {
			return "", nil, errors.New("no record")
		},
		HTTPGet: func(_ context.Context, _ string) ([]byte, error) {
			return nil, errors.New("404")
		},
		AutoconfigURLs: func(domain string) []string {
			return []string{"https://stub/" + domain}
		},
	}
	_, ok, _ := discoverSMTP(context.Background(),
		"anything.unknown:993", "me@example.com", dd)
	if ok {
		t.Error("expected no candidate")
	}
}

func TestEnvConfigForParses(t *testing.T) {
	t.Setenv("GITFS_IMAP_ME_EXAMPLE_COM_CONFIG",
		`{"smtp_host":"smtp.example.com","smtp_port":465,"smtp_password":"secret"}`)
	ov, ok, err := envConfigFor("me@example.com")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ov.SMTPHost != "smtp.example.com" || ov.SMTPPort != 465 ||
		ov.SMTPPassword != "secret" {
		t.Errorf("ov = %+v", ov)
	}
}

func TestEnvConfigForAbsent(t *testing.T) {
	os.Unsetenv("GITFS_IMAP_ME_EXAMPLE_COM_CONFIG")
	_, ok, err := envConfigFor("me@example.com")
	if err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestEnvConfigForBadJSON(t *testing.T) {
	t.Setenv("GITFS_IMAP_ME_EXAMPLE_COM_CONFIG", "not-json")
	_, _, err := envConfigFor("me@example.com")
	if err == nil {
		t.Error("expected error on bad JSON")
	}
}

func TestMergeOverrideFillsBlobGaps(t *testing.T) {
	blob := credsBlob{SMTPHost: "keyring.example.com", SMTPPort: 587}
	ov := envConfigOverride{SMTPPassword: "envpw"}
	ep, user, pw, _ := mergeOverride(blob, ov)
	if ep.Host != "keyring.example.com" || ep.Port != 587 {
		t.Errorf("ep = %+v", ep)
	}
	if pw != "envpw" {
		t.Errorf("pw = %q", pw)
	}
	if user != "" {
		t.Errorf("user = %q", user)
	}
}

func TestMergeOverrideOverridesHost(t *testing.T) {
	blob := credsBlob{SMTPHost: "keyring.example.com", SMTPPort: 587}
	ov := envConfigOverride{SMTPHost: "env.example.com", SMTPPort: 465}
	ep, _, _, mutated := mergeOverride(blob, ov)
	if !mutated {
		t.Error("expected mutated=true")
	}
	if ep.Host != "env.example.com" || ep.Port != 465 || ep.Mode != "tls" {
		t.Errorf("ep = %+v", ep)
	}
}

func TestCredsBlobAdditiveMigration(t *testing.T) {
	// Old-shape blob: no SMTP fields.
	old := []byte(`{"type":"basic","username":"me@ex.com","password":"p"}`)
	var b credsBlob
	if err := json.Unmarshal(old, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.Username != "me@ex.com" || b.Password != "p" {
		t.Errorf("blob = %+v", b)
	}
	if b.SMTPHost != "" || b.SMTPPort != 0 {
		t.Errorf("older blob must not produce SMTP fields; got %+v", b)
	}
	// Round-trip after SMTP fields added.
	b.SMTPHost = "smtp.ex.com"
	b.SMTPPort = 587
	b.SMTPMode = "starttls"
	b.SMTPUser = "me@ex.com"
	out, _ := json.Marshal(b)
	var b2 credsBlob
	if err := json.Unmarshal(out, &b2); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if b2.SMTPHost != "smtp.ex.com" || b2.SMTPPort != 587 {
		t.Errorf("round-tripped: %+v", b2)
	}
}

func TestParseOutgoingMessageValid(t *testing.T) {
	raw := []byte("From: me@ex.com\r\n" +
		"To: you@ex.com, other@ex.com\r\n" +
		"Subject: hi\r\n" +
		"Date: Mon, 19 Apr 2026 12:00:00 +0000\r\n" +
		"\r\nbody\r\n")
	p, err := parseOutgoingMessage(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.From != "me@ex.com" {
		t.Errorf("from = %q", p.From)
	}
	if len(p.Recipients) != 2 ||
		p.Recipients[0] != "you@ex.com" || p.Recipients[1] != "other@ex.com" {
		t.Errorf("recipients = %+v", p.Recipients)
	}
	if p.Subject != "hi" {
		t.Errorf("subject = %q", p.Subject)
	}
}

func TestParseOutgoingMessageCombinesCcBcc(t *testing.T) {
	raw := []byte("From: me@ex.com\r\n" +
		"To: you@ex.com\r\n" +
		"Cc: cc@ex.com\r\n" +
		"Bcc: bcc@ex.com\r\n" +
		"Subject: hi\r\n\r\nbody\r\n")
	p, err := parseOutgoingMessage(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Recipients) != 3 {
		t.Errorf("expected 3 recipients, got %+v", p.Recipients)
	}
}

func TestParseOutgoingMessageDedupesRecipients(t *testing.T) {
	raw := []byte("From: me@ex.com\r\n" +
		"To: you@ex.com\r\n" +
		"Cc: you@ex.com, other@ex.com\r\n\r\nbody\r\n")
	p, err := parseOutgoingMessage(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Recipients) != 2 {
		t.Errorf("expected dedupe, got %+v", p.Recipients)
	}
}

func TestParseOutgoingMessageMissingFrom(t *testing.T) {
	raw := []byte("To: you@ex.com\r\nSubject: hi\r\n\r\nbody\r\n")
	_, err := parseOutgoingMessage(raw)
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseOutgoingMessageMissingRecipients(t *testing.T) {
	raw := []byte("From: me@ex.com\r\nSubject: hi\r\n\r\nbody\r\n")
	_, err := parseOutgoingMessage(raw)
	if err == nil {
		t.Error("expected error")
	}
}

func TestApplyMessageDefaultsInjectsSubject(t *testing.T) {
	raw := []byte("From: me@ex.com\r\nTo: you@ex.com\r\n\r\nbody\r\n")
	out, subject, err := applyMessageDefaults(raw, "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "hello world" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(string(out), "Subject: hello world\r\n") {
		t.Errorf("out missing Subject header: %q", out)
	}
}

func TestApplyMessageDefaultsKeepsExistingSubject(t *testing.T) {
	raw := []byte("From: me@ex.com\r\nTo: you@ex.com\r\n" +
		"Subject: keep\r\n\r\nbody\r\n")
	out, subject, err := applyMessageDefaults(raw, "overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "keep" || string(out) != string(raw) {
		t.Errorf("defaults clobbered existing subject; out=%q subject=%q", out, subject)
	}
}

func TestEnsureMessageIDStampsWhenMissing(t *testing.T) {
	raw := []byte("From: me@ex.com\r\n\r\nbody\r\n")
	ts := time.Unix(1_700_000_000, 0).UTC()
	out, id := ensureMessageID(raw, "", "smtp.ex.com", ts)
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@smtp.ex.com>") {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(string(out), "Message-ID: "+id+"\r\n") {
		t.Errorf("header not injected: %q", out)
	}
}

func TestEnsureMessageIDPreservesExisting(t *testing.T) {
	raw := []byte("From: me@ex.com\r\n\r\nbody\r\n")
	out, id := ensureMessageID(raw, "<abc@host>", "smtp.ex.com", time.Now())
	if id != "<abc@host>" || string(out) != string(raw) {
		t.Errorf("preservation failed")
	}
}

func TestSentPathFor(t *testing.T) {
	d := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	got := sentPathFor(d, "<abc.123@ex.com>")
	want := "sent/2026/04/19/abc.123@ex.com.eml"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSentPathForZeroDate(t *testing.T) {
	var zero time.Time
	got := sentPathFor(zero, "<x@y>")
	// zero time formats to "0001/01/01" — function just uses the
	// provided date literally; caller substitutes a real time before
	// calling. Assert the rest of the path is sensible.
	if !strings.HasSuffix(got, "/x@y.eml") {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"abc@host":       "abc@host",
		"a/b":            "a-b",
		"../weird":       "..-weird",
		"spaces are bad": "spaces-are-bad",
		"":               "",
		"!@#allows@only": "@-allows@only",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestDomainOf(t *testing.T) {
	if got := domainOf("me@example.com"); got != "example.com" {
		t.Errorf("got %q", got)
	}
	if got := domainOf("no-at"); got != "no-at" {
		t.Errorf("got %q", got)
	}
}

func TestBlobEqualDetectsChange(t *testing.T) {
	a := credsBlob{Username: "u", Password: "p"}
	b := a
	if !blobEqual(a, b) {
		t.Error("equal blobs not detected")
	}
	b.SMTPHost = "x"
	if blobEqual(a, b) {
		t.Error("changed SMTPHost should not be equal")
	}
}
