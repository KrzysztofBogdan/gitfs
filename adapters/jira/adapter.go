package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/adapter"
)

// Adapter implements adapter.Adapter against Jira Cloud.
type Adapter struct {
	site    string
	project string
	baseURL string
}

func newAdapter(u adapter.URL) (adapter.Adapter, error) {
	if u.Account == "" {
		return nil, fmt.Errorf(
			"jira:// URL must include a site slug (jira://<site>/<PROJ>)")
	}
	if u.Path == "" {
		return nil, fmt.Errorf(
			"jira:// URL must include a project key (jira://<site>/<PROJ>)")
	}
	project := strings.SplitN(u.Path, "/", 2)[0]
	return &Adapter{
		site:    u.Account,
		project: project,
		baseURL: fmt.Sprintf("https://%s.atlassian.net", u.Account),
	}, nil
}

// Info returns public adapter metadata.
func (a *Adapter) Info() adapter.Info {
	return adapter.Info{
		Scheme:       "jira",
		AuthStrategy: adapter.AuthTokenPaste,
		AuthPrompt:   "Jira API token",
	}
}

type credsBlob struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// Authenticate resolves credentials: env first, then interactive prompt.
func (a *Adapter) Authenticate(
	_ context.Context, prior adapter.Credentials, io adapter.IO,
) (adapter.Credentials, adapter.Persist, error) {
	if prior != nil {
		return prior, false, nil
	}
	slug := envSiteSlug(a.site)
	email, haveE := os.LookupEnv("GITFS_JIRA_" + slug + "_EMAIL")
	token, haveT := os.LookupEnv("GITFS_JIRA_" + slug + "_TOKEN")
	if haveE && haveT {
		blob, _ := json.Marshal(credsBlob{Type: "basic", Username: email, Token: token})
		return blob, false, nil
	}
	if !io.IsTTY() {
		return nil, false, adapter.ErrNeedsInteractive
	}
	email, err := io.ReadLine(fmt.Sprintf("Atlassian email for %s: ", a.site))
	if err != nil {
		return nil, false, err
	}
	token, err = io.ReadSecret(fmt.Sprintf(
		"Jira API token for %s "+
			"(create at https://id.atlassian.com/manage-profile/security/api-tokens): ",
		a.site))
	if err != nil {
		return nil, false, err
	}
	email = strings.TrimSpace(email)
	token = strings.TrimSpace(token)
	blob, _ := json.Marshal(credsBlob{Type: "basic", Username: email, Token: token})
	return blob, true, nil
}

// Pull does a full refresh: the Jira adapter re-runs its Fetch flow and
// offers every ticket's files as File proposals. The core classifies
// clean paths as applied no-ops. Incremental pull (via updated >= token)
// is left for a later revision.
func (a *Adapter) Pull(
	ctx context.Context, creds adapter.Credentials, _ []byte, emit adapter.PullEmitter,
) ([]byte, error) {
	return adapter.FullRefreshPull(ctx, a.Fetch, creds, emit)
}

// Commit is not yet wired for the Jira adapter. Every request is
// rejected; the core reports them in the per-path summary and exits 1.
func (a *Adapter) Commit(
	_ context.Context, _ adapter.Credentials,
	reqs []adapter.CommitRequest, emit adapter.CommitEmitter, _ adapter.IO,
) error {
	for _, r := range reqs {
		if err := emit.Reject(r.Path, "jira commit not implemented in v1"); err != nil {
			return err
		}
	}
	return nil
}

func envSiteSlug(s string) string {
	var b strings.Builder
	lastU := false
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastU = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
			lastU = false
		default:
			if !lastU {
				b.WriteByte('_')
				lastU = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
