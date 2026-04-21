package imap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

// envConfigOverride is the per-account JSON override for SMTP fields
// (spec §5). Each field set in the JSON wins over the keyring equivalent
// and suppresses discovery of that field.
type envConfigOverride struct {
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     int    `json:"smtp_port,omitempty"`
	SMTPUser     string `json:"smtp_user,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	SMTPMode     string `json:"smtp_mode,omitempty"`
}

func envConfigFor(account string) (envConfigOverride, bool, error) {
	key := "GITFS_IMAP_" + envAccountSlug(account) + "_CONFIG"
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return envConfigOverride{}, false, nil
	}
	var ov envConfigOverride
	if err := json.Unmarshal([]byte(raw), &ov); err != nil {
		return envConfigOverride{}, true, fmt.Errorf(
			"%s: %w (expected JSON object)", key, err)
	}
	return ov, true, nil
}

// resolveSendEndpoint determines the SMTP endpoint and password to use,
// running discovery and prompting where needed. Returns the final blob
// and a hasEnvOverride flag — when true, spec §5 requires the caller
// to suppress all keyring persistence of the resolved state.
func (a *Adapter) resolveSendEndpoint(
	ctx context.Context, blob credsBlob, io adapter.IO,
) (credsBlob, smtpEndpoint, string, bool, error) {
	override, hasOverride, err := envConfigFor(a.account)
	if err != nil {
		return blob, smtpEndpoint{}, "", false, err
	}
	ep, user, pw, _ := mergeOverride(blob, override)

	// Discovery only if no endpoint supplied by blob or override.
	if ep.Host == "" {
		dd := a.discovery
		if dd.TotalBudget == 0 {
			dd = defaultDiscoveryDeps()
		}
		found, ok, derr := discoverSMTP(ctx, a.host, a.account, dd)
		if derr != nil {
			return blob, smtpEndpoint{}, "", false, derr
		}
		if !ok {
			if !io.IsTTY() {
				return blob, smtpEndpoint{}, "", false, fmt.Errorf(
					"could not discover SMTP endpoint for %s; "+
						"set GITFS_IMAP_%s_CONFIG='{\"smtp_host\":\"...\",\"smtp_port\":587}' "+
						"or run interactively",
					domainOf(a.account),
					envAccountSlug(a.account))
			}
			fmt.Fprintf(io.Stderr(),
				"gitfs: could not discover SMTP endpoint for %s\n",
				domainOf(a.account))
			manual, perr := promptEndpoint(io)
			if perr != nil {
				return blob, smtpEndpoint{}, "", false, perr
			}
			ep = manual
		} else {
			ep = found
		}
	}
	if user == "" {
		user = a.account
	}
	if pw == "" {
		pw = blob.smtpPasswordOrShared()
	}

	// Fold the resolved endpoint into the blob. The caller still decides
	// whether to persist; env-override callers must suppress persistence.
	if ep.Host != "" {
		blob.SMTPHost = ep.Host
		blob.SMTPPort = ep.Port
		blob.SMTPMode = ep.Mode
	}
	if user != "" {
		blob.SMTPUser = user
	}
	return blob, ep, pw, hasOverride, nil
}

// mergeOverride applies env-CONFIG overrides on top of a credsBlob and
// returns (ep, user, pw, mutated). mutated is true if any field was
// supplied — callers use it as a prompt for keyring persistence, but
// env overrides themselves must not persist.
func mergeOverride(blob credsBlob, ov envConfigOverride) (smtpEndpoint, string, string, bool) {
	ep := smtpEndpoint{
		Host: blob.SMTPHost,
		Port: blob.SMTPPort,
		Mode: blob.SMTPMode,
	}
	mutated := false
	if ov.SMTPHost != "" {
		ep.Host = ov.SMTPHost
		mutated = true
	}
	if ov.SMTPPort != 0 {
		ep.Port = ov.SMTPPort
		mutated = true
	}
	if ov.SMTPMode != "" {
		ep.Mode = ov.SMTPMode
	}
	if ep.Port != 0 && ep.Mode == "" {
		if ep.Port == 465 {
			ep.Mode = "tls"
		} else {
			ep.Mode = "starttls"
		}
	}
	user := blob.SMTPUser
	if ov.SMTPUser != "" {
		user = ov.SMTPUser
	}
	pw := ""
	if ov.SMTPPassword != "" {
		pw = ov.SMTPPassword
	}
	return ep, user, pw, mutated
}

// promptEndpoint asks for smtp_host + smtp_port on a TTY. Called only
// when discovery failed entirely.
func promptEndpoint(io adapter.IO) (smtpEndpoint, error) {
	host, err := io.ReadLine("SMTP host: ")
	if err != nil {
		return smtpEndpoint{}, err
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return smtpEndpoint{}, fmt.Errorf("SMTP host required")
	}
	portS, err := io.ReadLine("SMTP port [587]: ")
	if err != nil {
		return smtpEndpoint{}, err
	}
	portS = strings.TrimSpace(portS)
	port := 587
	if portS != "" {
		var p int
		if _, err := fmt.Sscanf(portS, "%d", &p); err == nil && p > 0 {
			port = p
		}
	}
	mode := "starttls"
	if port == 465 {
		mode = "tls"
	}
	return smtpEndpoint{Host: host, Port: port, Mode: mode}, nil
}

func domainOf(account string) string {
	if at := strings.LastIndexByte(account, '@'); at >= 0 {
		return account[at+1:]
	}
	return account
}

// Commit sends each request as an RFC 5322 message via SMTP. Accepted
// messages are rewritten from drafts/* to sent/YYYY/MM/DD/<msg-id>.eml;
// the adapter rejects any deletion request (mail is not deleted via
// this flow). Parse failures and connection failures become per-path
// rejections; auth-level failures abort with an error so the user can
// fix credentials once.
func (a *Adapter) Commit(
	ctx context.Context, creds adapter.Credentials,
	reqs []adapter.CommitRequest, emit adapter.CommitEmitter, io adapter.IO,
) error {
	if len(reqs) == 0 {
		return nil
	}
	var blob credsBlob
	if err := json.Unmarshal(creds, &blob); err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}

	// Separate deletions up front — the adapter cannot delete via SMTP
	// and the spec's send flow never produces deletions.
	var sends []adapter.CommitRequest
	for _, r := range reqs {
		if r.Kind == adapter.CommitDeletion {
			if err := emit.Reject(r.Path,
				"imap adapter does not delete sent mail"); err != nil {
				return err
			}
			continue
		}
		sends = append(sends, r)
	}
	if len(sends) == 0 {
		return nil
	}

	newBlob, ep, pw, hasEnvOverride, err := a.resolveSendEndpoint(ctx, blob, io)
	if err != nil {
		return err
	}

	sender, _, newBlob, err := a.openSender(ctx, ep, newBlob, pw, io)
	if err != nil {
		return err
	}
	defer sender.Close()

	for _, r := range sends {
		if err := a.sendOne(sender, r, emit); err != nil {
			return err
		}
	}

	// Fold the successful blob update into the emitter. Spec §5 forbids
	// caching any discovery / password state when env-var credentials
	// are in effect — suppress the round-trip in that case. The core
	// also gates persistence on keyring origin, so env-only TOKEN usage
	// without a CONFIG override is still safe.
	if !hasEnvOverride && !blobEqual(blob, newBlob) {
		out, mErr := json.Marshal(newBlob)
		if mErr == nil {
			_ = emit.UpdateCredentials(out)
		}
	}
	return nil
}

// openSender connects and authenticates, falling back to a password
// prompt when the shared IMAP password is rejected (spec §4 step 4).
// On success, returns a live sender, the password used, and the blob
// with smtp_password filled when a prompt was required.
func (a *Adapter) openSender(
	ctx context.Context, ep smtpEndpoint, blob credsBlob, pw string, io adapter.IO,
) (*smtpSender, string, credsBlob, error) {
	user := blob.SMTPUser
	if user == "" {
		user = a.account
	}
	sender, err := dialSMTP(ctx, ep, user, pw)
	if err == nil {
		return sender, pw, blob, nil
	}
	var authErr *smtpAuthError
	if !errors.As(err, &authErr) {
		return nil, "", blob, err
	}

	// Auth failed on shared password — prompt if we can, else surface.
	if !io.IsTTY() {
		return nil, "", blob, fmt.Errorf(
			"SMTP auth failed on %s; "+
				"set GITFS_IMAP_%s_CONFIG='{\"smtp_password\":\"...\"}' "+
				"or run interactively",
			ep.addr(), envAccountSlug(a.account))
	}
	fmt.Fprintf(io.Stderr(),
		"gitfs: %s rejected the IMAP password\n", ep.addr())
	newPw, perr := io.ReadSecret("SMTP password: ")
	if perr != nil {
		return nil, "", blob, perr
	}
	newPw = strings.TrimSpace(newPw)
	if newPw == "" {
		return nil, "", blob, fmt.Errorf("SMTP password required")
	}
	sender, err = dialSMTP(ctx, ep, user, newPw)
	if err != nil {
		return nil, "", blob, err
	}
	blob.SMTPPassword = newPw
	return sender, newPw, blob, nil
}

// sendOne parses the request, sends via SMTP, and calls emit.Accept
// with the rename to sent/YYYY/MM/DD/<msg-id>.eml on success, or
// emit.Reject on parse or transport error.
func (a *Adapter) sendOne(
	sender *smtpSender, r adapter.CommitRequest, emit adapter.CommitEmitter,
) error {
	body, _, err := applyMessageDefaults(r.Content, r.Message)
	if err != nil {
		return emit.Reject(r.Path, fmt.Sprintf("parse message: %v", err))
	}
	parsed, err := parseOutgoingMessage(body)
	if err != nil {
		return emit.Reject(r.Path, err.Error())
	}
	now := time.Now().UTC()
	dateForPath := parsed.Date
	if dateForPath.IsZero() {
		dateForPath = now
	}
	body, msgID := ensureMessageID(body, parsed.MessageID, sender.host, now)

	if err := sender.Send(parsed.From, parsed.Recipients, body); err != nil {
		return emit.Reject(r.Path, fmt.Sprintf("smtp send: %v", err))
	}

	newPath := sentPathFor(dateForPath, msgID)
	return emit.Accept(r.Path, []adapter.CommitResult{
		{Path: newPath, Content: body},
	})
}

// sentPathFor builds the canonical sent/YYYY/MM/DD/<id>.eml path from a
// message's date and Message-ID. The message-id is stripped of angle
// brackets and sanitized for filesystem safety.
func sentPathFor(date time.Time, msgID string) string {
	d := date.UTC()
	year := fmt.Sprintf("%04d", d.Year())
	month := fmt.Sprintf("%02d", int(d.Month()))
	day := fmt.Sprintf("%02d", d.Day())
	id := strings.TrimSpace(msgID)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	id = sanitizeFilename(id)
	if id == "" {
		id = fmt.Sprintf("%d", d.UnixNano())
	}
	return path.Join("sent", year, month, day, id+".eml")
}

func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '@', r == '+':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func blobEqual(a, b credsBlob) bool {
	return a.Type == b.Type && a.Username == b.Username &&
		a.Password == b.Password && a.SMTPHost == b.SMTPHost &&
		a.SMTPPort == b.SMTPPort && a.SMTPUser == b.SMTPUser &&
		a.SMTPPassword == b.SMTPPassword && a.SMTPMode == b.SMTPMode
}
