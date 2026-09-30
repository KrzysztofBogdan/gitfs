package jira

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// apiUser is a user as Jira and the customer API return one.
type apiUser struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	AccountType  string `json:"accountType"`
	Active       bool   `json:"active"`
}

type person struct {
	Account string `json:"account"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
	Type    string `json:"type,omitempty"`
	Active  bool   `json:"active"`
}

const (
	peopleID   = "people"
	peoplePath = ".people.xml"
)

// registry is every user seen in the tree; it becomes .people.xml (jira spec §5.8).
type registry struct {
	m     map[string]person
	dirty bool // changed since load
}

func newRegistry() *registry { return &registry{m: map[string]person{}} }

// see records u; a later sighting fills in what an earlier one lacked.
func (r *registry) see(u apiUser) {
	if u.AccountID == "" {
		return
	}
	p := person{Account: u.AccountID, Name: u.DisplayName, Email: u.EmailAddress, Type: u.AccountType, Active: u.Active}
	if old, ok := r.m[p.Account]; ok {
		if p.Name == "" {
			p.Name = old.Name
		}
		if p.Email == "" {
			p.Email = old.Email
		}
		if p.Type == "" {
			p.Type, p.Active = old.Type, old.Active
		}
		if old == p {
			return
		}
	}
	r.m[p.Account] = p
	r.dirty = true
}

// seeMention records a user known only from a mention, unless known already.
func (r *registry) seeMention(id, text string) {
	if id == "" {
		return
	}
	if _, ok := r.m[id]; ok {
		return
	}
	r.m[id] = person{Account: id, Name: strings.TrimPrefix(text, "@"), Active: true}
	r.dirty = true
}

// seeMentions records the mentions anywhere in ns.
func (r *registry) seeMentions(ns []*xmltree.Node) {
	for _, n := range ns {
		if n.Kind != xmltree.Element {
			continue
		}
		if n.Name == "mention" {
			id, _ := n.Attr("id")
			text, _ := n.Attr("text")
			r.seeMention(id, text)
		}
		r.seeMentions(n.Children)
	}
}

func (r *registry) reset() {
	r.m = map[string]person{}
	r.dirty = true
}

// byEmail finds a known account by email, case-insensitively.
func (r *registry) byEmail(email string) (person, bool) {
	for _, p := range r.m {
		if p.Email != "" && strings.EqualFold(p.Email, email) {
			return p, true
		}
	}
	return person{}, false
}

func (r *registry) node() *xmltree.Node {
	ps := make([]person, 0, len(r.m))
	for _, p := range r.m {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].Account < ps[j].Account
	})
	root := el("people", "id", peopleID)
	for _, p := range ps {
		n := el("person", "account", p.Account, "type", p.Type, "active", strconv.FormatBool(p.Active), "email", p.Email)
		if p.Name != "" {
			n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(p.Name)}}
		}
		root.Children = append(root.Children, n)
	}
	return root
}

// resource is .people.xml; its version is a hash of the content.
func (r *registry) resource() adapter.Resource {
	root := r.node()
	return adapter.Resource{ID: peopleID, Version: contentHash(root), Path: peoplePath, Root: root}
}

func contentHash(root *xmltree.Node) string {
	sum := sha256.Sum256([]byte(xmltree.Print(root, 0)))
	return hex.EncodeToString(sum[:6])
}

func (r *registry) load(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ps []person
	if err := json.Unmarshal(data, &ps); err != nil {
		return err
	}
	for _, p := range ps {
		r.m[p.Account] = p
	}
	return nil
}

func (r *registry) save(path string) error {
	ps := make([]person, 0, len(r.m))
	for _, p := range r.m {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Account < ps[j].Account })
	data, err := json.MarshalIndent(ps, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// writeFileAtomic writes data to path through a temp file and a rename.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// userNode is <name account="…">Display Name</name>.
func userNode(name string, u apiUser) *xmltree.Node {
	n := el(name, "account", u.AccountID)
	if u.DisplayName != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(u.DisplayName)}}
	}
	return n
}
