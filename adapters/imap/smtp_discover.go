package imap

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// smtpEndpoint is a candidate SMTP host/port plus the connection mode the
// caller should use. Mode "starttls" means plain-text TCP + STARTTLS upgrade;
// "tls" means implicit TLS from the first byte.
type smtpEndpoint struct {
	Host string
	Port int
	Mode string // "starttls" or "tls"
}

func (e smtpEndpoint) addr() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// smtpStaticTable maps IMAP-host suffix → verified SMTP endpoint. Used as the
// first, zero-I/O discovery step (spec §3 mechanism 1). Hardcoded deliberately
// — the common-case list is short and changes rarely.
var smtpStaticTable = []struct {
	Suffix   string
	Endpoint smtpEndpoint
}{
	{"imap.gmail.com", smtpEndpoint{"smtp.gmail.com", 587, "starttls"}},
	{"outlook.office365.com", smtpEndpoint{"smtp.office365.com", 587, "starttls"}},
	{"imap.mail.me.com", smtpEndpoint{"smtp.mail.me.com", 587, "starttls"}},
	{"imap.fastmail.com", smtpEndpoint{"smtp.fastmail.com", 465, "tls"}},
	{"imap.mail.yahoo.com", smtpEndpoint{"smtp.mail.yahoo.com", 587, "starttls"}},
	{"imap.zoho.com", smtpEndpoint{"smtp.zoho.com", 587, "starttls"}},
	{"imap.protonmail.ch", smtpEndpoint{"smtp.protonmail.ch", 587, "starttls"}},
}

// discoveryDeps injects network-touching collaborators so discovery is
// testable without real DNS/HTTP/SMTP.
type discoveryDeps struct {
	LookupSRV      func(service, proto, name string) (string, []*net.SRV, error)
	HTTPGet        func(ctx context.Context, url string) ([]byte, error)
	Handshake      func(ctx context.Context, ep smtpEndpoint) error
	TotalBudget    time.Duration
	PerMechBudget  time.Duration
	AutoconfigURLs func(domain string) []string
}

func defaultDiscoveryDeps() discoveryDeps {
	return discoveryDeps{
		LookupSRV:      net.LookupSRV,
		HTTPGet:        httpGet,
		Handshake:      smtpHandshake,
		TotalBudget:    5 * time.Second,
		PerMechBudget:  2 * time.Second,
		AutoconfigURLs: defaultAutoconfigURLs,
	}
}

func defaultAutoconfigURLs(domain string) []string {
	return []string{
		"https://autoconfig." + domain + "/mail/config-v1.1.xml",
		"https://autoconfig.thunderbird.net/v1.1/" + domain,
	}
}

// discoverSMTP tries mechanisms in order, first verified candidate wins.
// "imapHost" is the full host:port form used for IMAP (e.g. "imap.gmail.com:993"),
// "email" is the user's address (e.g. "me@gmail.com"). Returns ok=false when
// no mechanism yielded a candidate that completed a TCP + TLS handshake
// within the total budget.
func discoverSMTP(
	ctx context.Context, imapHost, email string, dd discoveryDeps,
) (smtpEndpoint, bool, error) {
	imapHostOnly := imapHost
	if h, _, err := net.SplitHostPort(imapHost); err == nil {
		imapHostOnly = h
	}
	domain := ""
	if at := strings.LastIndexByte(email, '@'); at >= 0 {
		domain = strings.ToLower(email[at+1:])
	}

	deadline := time.Now().Add(dd.TotalBudget)
	tryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	candidates := staticTableCandidates(imapHostOnly)
	candidates = append(candidates, hostSubstitutionCandidates(imapHostOnly)...)

	// Mechanisms 1 and 2: direct handshake on the candidate(s).
	for _, ep := range candidates {
		if err := tryHandshake(tryCtx, dd, ep); err == nil {
			return ep, true, nil
		}
		if tryCtx.Err() != nil {
			return smtpEndpoint{}, false, nil
		}
	}

	// Mechanism 3: SRV lookup.
	if domain != "" && dd.LookupSRV != nil {
		ep, ok := srvCandidate(tryCtx, dd, domain)
		if ok {
			if err := tryHandshake(tryCtx, dd, ep); err == nil {
				return ep, true, nil
			}
		}
	}

	// Mechanism 4: Thunderbird autoconfig.
	if domain != "" && dd.HTTPGet != nil {
		for _, url := range dd.AutoconfigURLs(domain) {
			if tryCtx.Err() != nil {
				return smtpEndpoint{}, false, nil
			}
			ep, ok := autoconfigCandidate(tryCtx, dd, url)
			if !ok {
				continue
			}
			if err := tryHandshake(tryCtx, dd, ep); err == nil {
				return ep, true, nil
			}
		}
	}

	return smtpEndpoint{}, false, nil
}

// staticTableCandidates returns any static-table entries whose suffix matches
// the imap host. Usually zero or one hit.
func staticTableCandidates(imapHost string) []smtpEndpoint {
	out := []smtpEndpoint{}
	for _, e := range smtpStaticTable {
		if imapHost == e.Suffix || strings.HasSuffix(imapHost, "."+e.Suffix) {
			out = append(out, e.Endpoint)
		}
	}
	return out
}

// hostSubstitutionCandidates generates "smtp.<rest>" from imap./mail. prefixes,
// trying STARTTLS:587 then implicit TLS:465 (spec §3 mechanism 2).
func hostSubstitutionCandidates(imapHost string) []smtpEndpoint {
	var stem string
	switch {
	case strings.HasPrefix(imapHost, "imap."):
		stem = imapHost[len("imap."):]
	case strings.HasPrefix(imapHost, "mail."):
		stem = imapHost[len("mail."):]
	default:
		return nil
	}
	smtpHost := "smtp." + stem
	return []smtpEndpoint{
		{Host: smtpHost, Port: 587, Mode: "starttls"},
		{Host: smtpHost, Port: 465, Mode: "tls"},
	}
}

// srvCandidate runs an RFC 6186 _submission._tcp SRV lookup. Returns the
// highest-priority entry if any.
func srvCandidate(ctx context.Context, dd discoveryDeps, domain string) (smtpEndpoint, bool) {
	type result struct {
		recs []*net.SRV
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		_, recs, err := dd.LookupSRV("submission", "tcp", domain)
		ch <- result{recs: recs, err: err}
	}()
	select {
	case <-ctx.Done():
		return smtpEndpoint{}, false
	case r := <-ch:
		if r.err != nil || len(r.recs) == 0 {
			return smtpEndpoint{}, false
		}
		best := r.recs[0]
		for _, rec := range r.recs[1:] {
			if rec.Priority < best.Priority {
				best = rec
			}
		}
		host := strings.TrimSuffix(best.Target, ".")
		if host == "" {
			return smtpEndpoint{}, false
		}
		mode := "starttls"
		if best.Port == 465 {
			mode = "tls"
		}
		return smtpEndpoint{Host: host, Port: int(best.Port), Mode: mode}, true
	}
}

// autoconfigCandidate fetches and parses a Thunderbird-style XML document
// and returns the first SMTP endpoint advertised.
func autoconfigCandidate(ctx context.Context, dd discoveryDeps, url string) (smtpEndpoint, bool) {
	subCtx, cancel := context.WithTimeout(ctx, dd.PerMechBudget)
	defer cancel()
	body, err := dd.HTTPGet(subCtx, url)
	if err != nil {
		return smtpEndpoint{}, false
	}
	return parseAutoconfig(body)
}

// autoconfigDoc is a minimal subset of the Thunderbird autoconfig schema:
// https://wiki.mozilla.org/Thunderbird:Autoconfiguration:ConfigFileFormat
type autoconfigDoc struct {
	EmailProvider struct {
		Outgoing []struct {
			Type       string `xml:"type,attr"`
			Hostname   string `xml:"hostname"`
			Port       int    `xml:"port"`
			SocketType string `xml:"socketType"`
		} `xml:"outgoingServer"`
	} `xml:"emailProvider"`
}

func parseAutoconfig(body []byte) (smtpEndpoint, bool) {
	var doc autoconfigDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return smtpEndpoint{}, false
	}
	for _, s := range doc.EmailProvider.Outgoing {
		if !strings.EqualFold(s.Type, "smtp") || s.Hostname == "" || s.Port == 0 {
			continue
		}
		mode := "starttls"
		if strings.EqualFold(s.SocketType, "SSL") || s.Port == 465 {
			mode = "tls"
		}
		return smtpEndpoint{Host: s.Hostname, Port: s.Port, Mode: mode}, true
	}
	return smtpEndpoint{}, false
}

func tryHandshake(ctx context.Context, dd discoveryDeps, ep smtpEndpoint) error {
	subCtx, cancel := context.WithTimeout(ctx, dd.PerMechBudget)
	defer cancel()
	return dd.Handshake(subCtx, ep)
}

// smtpHandshake opens TCP + (STARTTLS | implicit TLS) and returns nil on
// successful EHLO/TLS. It does not attempt AUTH.
func smtpHandshake(ctx context.Context, ep smtpEndpoint) error {
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", ep.addr())
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if ep.Mode == "tls" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: ep.Host})
		if err := tlsConn.Handshake(); err != nil {
			return err
		}
		client, err := smtp.NewClient(tlsConn, ep.Host)
		if err != nil {
			return err
		}
		_ = client.Quit()
		return nil
	}
	client, err := smtp.NewClient(conn, ep.Host)
	if err != nil {
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		_ = client.Quit()
		return fmt.Errorf("%s: server does not advertise STARTTLS", ep.addr())
	}
	if err := client.StartTLS(&tls.Config{ServerName: ep.Host}); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256*1024))
}
