package imap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// folderProbes maps logical folder name → mailbox candidates (spec §6.3).
var folderProbes = []struct {
	Logical    string
	Candidates []string
}{
	{"inbox", []string{"INBOX"}},
	{"sent", []string{"Sent", "Sent Mail", "Sent Items", "[Gmail]/Sent Mail"}},
	{"drafts", []string{"Drafts", "[Gmail]/Drafts"}},
}

// Fetch connects to the IMAP host, walks INBOX/Sent/Drafts, renders each
// message to markdown, and returns a per-folder HEAD token.
func (a *Adapter) Fetch(
	ctx context.Context, creds adapter.Credentials, emit adapter.Emitter,
) ([]byte, error) {
	var blob credsBlob
	if err := json.Unmarshal(creds, &blob); err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	c, err := imapclient.DialTLS(a.host, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", a.host, err)
	}
	defer c.Close()

	if err := c.Login(blob.Username, blob.Password).Wait(); err != nil {
		return nil, fmt.Errorf("imap login: %w", err)
	}

	head := map[string]folderHead{}
	for _, fp := range folderProbes {
		mbox, picked, err := selectFirstExisting(c, fp.Candidates)
		if err != nil {
			return nil, err
		}
		if mbox == nil {
			continue
		}
		h, err := fetchFolder(ctx, c, fp.Logical, mbox, emit)
		if err != nil {
			return nil, fmt.Errorf("fetch %s (%s): %w", fp.Logical, picked, err)
		}
		head[fp.Logical] = h
	}

	return json.Marshal(head)
}

type folderHead struct {
	UIDValidity uint32 `json:"uidvalidity"`
	UIDNext     uint32 `json:"uidnext"`
}

// plan is the lightweight per-message record built in pass 1 so pass 2 can
// emit with a pre-assigned, collision-resolved filename and mtime (§6).
type plan struct {
	UID              goimap.UID
	Date             time.Time
	SanitizedSubject string
	Filename         string
}

// selectFirstExisting SELECTs the first candidate that exists; missing
// mailboxes are skipped silently per spec §6.3.
func selectFirstExisting(
	c *imapclient.Client, candidates []string,
) (*goimap.SelectData, string, error) {
	for _, name := range candidates {
		data, err := c.Select(name, nil).Wait()
		if err == nil {
			return data, name, nil
		}
	}
	return nil, "", nil
}

func fetchFolder(
	_ context.Context, c *imapclient.Client, logical string,
	mbox *goimap.SelectData, emit adapter.Emitter,
) (folderHead, error) {
	if mbox.NumMessages == 0 {
		return folderHead{
			UIDValidity: mbox.UIDValidity,
			UIDNext:     uint32(mbox.UIDNext),
		}, nil
	}

	plans, err := planFolder(c)
	if err != nil {
		return folderHead{}, err
	}
	assignFilenames(plans)

	if err := renderFolder(c, logical, plans, emit); err != nil {
		return folderHead{}, err
	}
	return folderHead{
		UIDValidity: mbox.UIDValidity,
		UIDNext:     uint32(mbox.UIDNext),
	}, nil
}

// planFolder is pass 1: fetch envelopes only and build a plan per UID.
func planFolder(c *imapclient.Client) ([]*plan, error) {
	var uids goimap.UIDSet
	uids.AddRange(1, 0)
	opts := &goimap.FetchOptions{
		Envelope:     true,
		InternalDate: true,
		UID:          true,
	}
	cmd := c.Fetch(uids, opts)
	var out []*plan
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			_ = cmd.Close()
			return nil, err
		}
		out = append(out, planFromBuf(buf))
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func planFromBuf(buf *imapclient.FetchMessageBuffer) *plan {
	var sub string
	if buf.Envelope != nil {
		sub = sanitizeSubject(buf.Envelope.Subject)
	} else {
		sub = sanitizeSubject("")
	}
	d := time.Time{}
	if buf.Envelope != nil {
		d = buf.Envelope.Date
	}
	if d.IsZero() {
		d = buf.InternalDate
	}
	return &plan{UID: buf.UID, Date: d, SanitizedSubject: sub}
}

// assignFilenames groups by SanitizedSubject, sorts each group by
// (Date asc, UID asc), and sets Filename: first member gets "<sub>.md",
// k-th (k ≥ 1) gets "<sub>-<k+1>.md" (spec §3.3, §3.4).
func assignFilenames(plans []*plan) {
	groups := map[string][]*plan{}
	for _, p := range plans {
		groups[p.SanitizedSubject] = append(groups[p.SanitizedSubject], p)
	}
	for _, g := range groups {
		sort.SliceStable(g, func(i, j int) bool {
			if !g[i].Date.Equal(g[j].Date) {
				return g[i].Date.Before(g[j].Date)
			}
			return g[i].UID < g[j].UID
		})
		for i, p := range g {
			if i == 0 {
				p.Filename = p.SanitizedSubject + ".md"
				continue
			}
			p.Filename = fmt.Sprintf("%s-%d.md", p.SanitizedSubject, i+1)
		}
	}
}

// renderFolder is pass 2: fetch full bodies and emit via the pre-assigned
// filename. Drift between passes is tolerated: a UID with no plan record
// is skipped (spec §6).
func renderFolder(
	c *imapclient.Client, logical string, plans []*plan, emit adapter.Emitter,
) error {
	byUID := make(map[goimap.UID]*plan, len(plans))
	for _, p := range plans {
		byUID[p.UID] = p
	}
	var uids goimap.UIDSet
	uids.AddRange(1, 0)
	opts := &goimap.FetchOptions{
		Envelope:     true,
		InternalDate: true,
		UID:          true,
		BodySection:  []*goimap.FetchItemBodySection{{Peek: true}},
	}
	cmd := c.Fetch(uids, opts)
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		buf, err := msg.Collect()
		if err != nil {
			_ = cmd.Close()
			return err
		}
		p, ok := byUID[buf.UID]
		if !ok {
			continue
		}
		mtime := p.Date
		if err := renderMessage(emit, logical, p.Filename, mtime, buf); err != nil {
			_ = cmd.Close()
			return err
		}
	}
	return cmd.Close()
}
