package ovh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// recordTypes is OVH's domain.zone.RecordTypeEnum (/1.0/domain.json).
var recordTypes = map[string]bool{"A": true, "AAAA": true, "CAA": true, "CNAME": true, "DKIM": true, "DMARC": true, "DNAME": true,
	"HTTPS": true, "LOC": true, "MX": true, "NAPTR": true, "NS": true, "PTR": true, "RP": true, "SPF": true, "SRV": true,
	"SSHFP": true, "SVCB": true, "TLSA": true, "TXT": true}

var redirectTypes = map[string]bool{"visible": true, "visiblePermanent": true, "invisible": true}

const (
	warnNS     = "changes the zone's own NS records: a mistake makes the whole zone unreachable"
	warnSOA    = "SOA timers decide how long resolvers keep stale answers; check the values before sending"
	warnDNSOff = "if the registrar publishes a DS record for this domain, remove it first or the domain stops resolving"
	warnDNSOn  = "once DNSSEC is active the registrar must publish the DS record; OVH does it only for domains it registers"
)

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return n.TextContent()
}

func attr(n *xmltree.Node, k string) string {
	if n == nil {
		return ""
	}
	v, _ := n.Attr(k)
	return v
}

// nthNew is the nth child named name without the identity attribute.
func nthNew(root *xmltree.Node, name, idAttr string, nth int) *xmltree.Node {
	i := 0
	for _, c := range root.ChildrenNamed(name) {
		if _, ok := c.Attr(idAttr); !ok {
			if i++; i == nth {
				return c
			}
		}
	}
	return nil
}

// elems returns the local and base element an action targets.
func elems(a adapter.Action, local, base *xmltree.Node) (name string, ln, bn *xmltree.Node) {
	if a.Target == "" {
		return a.Group, child(local, a.Group), child(base, a.Group)
	}
	name, id, nth, _ := changes.ParseTarget(a.Target)
	e := schema.Find(zoneSchema.Elems, name)
	if e == nil {
		return name, nil, nil
	}
	if id != "" {
		return name, validate.FindSub(local, name, e.ID, id), validate.FindSub(base, name, e.ID, id)
	}
	if local != nil {
		ln = nthNew(local, name, e.ID, nth)
	}
	return name, ln, nil
}

func child(n *xmltree.Node, name string) *xmltree.Node {
	if n == nil || name == "" {
		return nil
	}
	return n.Child(name)
}

func summary(name string, n *xmltree.Node) string {
	if n == nil {
		return name
	}
	switch name {
	case "record":
		s := fmt.Sprintf("record %s %s %s", attr(n, "name"), attr(n, "type"), textOf(n))
		if t := attr(n, "ttl"); t != "" {
			s += " ttl " + t
		}
		return s
	case "redirect":
		return fmt.Sprintf("redirect %s → %s (%s)", attr(n, "name"), textOf(n), attr(n, "type"))
	case "dynhost":
		return fmt.Sprintf("dynhost %s %s", attr(n, "name"), textOf(n))
	case "dynhost-login":
		l := attr(n, "login")
		if l == "" {
			l = "<zone>-" + attr(n, "suffix")
		}
		return fmt.Sprintf("dynhost-login %s for %s", l, attr(n, "name"))
	}
	return name
}

func soaText(n *xmltree.Node) string {
	var parts []string
	for _, k := range []string{"ttl", "refresh", "expire", "nx-domain-ttl", "email"} {
		parts = append(parts, k+"="+attr(n, k))
	}
	return strings.Join(parts, " ")
}

// describe sets an action's class (always dns: every DNS write asks),
// detail and warning (DNS spec §7.4).
func describe(a *adapter.Action, local, base *xmltree.Node) {
	a.Class = "dns"
	name, ln, bn := elems(*a, local, base)
	switch {
	case a.Target == "" && a.Group == "":
		a.Detail = a.Verb + " zone (not supported: zones come with their domain)"
	case name == "soa":
		a.Detail = "update soa " + soaText(bn) + " → " + soaText(ln)
		a.Warning = warnSOA
	case name == "dnssec":
		a.Detail = "dnssec " + textOf(bn) + " → " + textOf(ln)
		switch textOf(ln) {
		case "disabled":
			a.Warning = warnDNSOff
		case "enabled":
			a.Warning = warnDNSOn
		}
	case a.Verb == "create":
		a.Detail = "create " + summary(name, ln)
	case a.Verb == "delete":
		a.Detail = "delete " + summary(name, bn)
	case name == "record" && bn != nil && ln != nil && attr(bn, "name") == attr(ln, "name") && attr(bn, "type") == attr(ln, "type"):
		a.Detail = fmt.Sprintf("update record %s %s %s → %s", attr(ln, "name"), attr(ln, "type"), textOf(bn), textOf(ln))
		if attr(bn, "ttl") != attr(ln, "ttl") {
			a.Detail += fmt.Sprintf(" (ttl %s → %s)", or(attr(bn, "ttl"), "default"), or(attr(ln, "ttl"), "default"))
		}
	default:
		a.Detail = "update " + summary(name, bn) + " → " + summary(name, ln)
	}
	if name == "record" && (isApexNS(ln) || isApexNS(bn)) {
		a.Warning = warnNS
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func isApexNS(n *xmltree.Node) bool {
	return n != nil && attr(n, "name") == "@" && attr(n, "type") == "NS"
}

func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource) {
	var root *xmltree.Node
	if local != nil {
		root = local.Root
	}
	describe(a, root, nil)
}

func (*Adapter) DescribeBase(a *adapter.Action, local, base *adapter.Resource) {
	var l, b *xmltree.Node
	if local != nil {
		l = local.Root
	}
	if base != nil {
		b = base.Root
	}
	describe(a, l, b)
}

var (
	mxRe  = regexp.MustCompile(`^(\d+) (\S+)$`)
	srvRe = regexp.MustCompile(`^(\d+) (\d+) (\d+) (\S+)$`)
	sufRe = regexp.MustCompile(`^[a-z0-9-]+$`)
)

func checkNum(label, v string) error {
	if v == "" {
		return nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 0 {
		return fmt.Errorf("%s %q is not a number of seconds", label, v)
	}
	return nil
}

func checkLossy(vals ...string) error {
	for _, v := range vals {
		if dnsx.Lossy(v) {
			return errors.New("the value holds characters XML cannot hold (shown as �); change it in the OVH control panel")
		}
	}
	return nil
}

func checkValue(typ, v string) error {
	switch typ {
	case "A":
		return dnsx.CheckIPv4(v)
	case "AAAA":
		return dnsx.CheckIPv6(v)
	case "CNAME", "NS", "PTR", "DNAME":
		return dnsx.CheckHost(v)
	case "MX":
		m := mxRe.FindStringSubmatch(v)
		if m == nil {
			return fmt.Errorf("MX value %q is not \"<priority> <host>\"", v)
		}
		return dnsx.CheckHost(m[2])
	case "SRV":
		m := srvRe.FindStringSubmatch(v)
		if m == nil {
			return fmt.Errorf("SRV value %q is not \"<priority> <weight> <port> <host>\"", v)
		}
		return dnsx.CheckHost(m[4])
	}
	if v == "" {
		return fmt.Errorf("%s record without a value", typ)
	}
	return nil
}

// cnameErrs maps record elements of local to their CNAME-rule error; a
// redirect or DynHost holds an A record at its name.
func cnameErrs(local *xmltree.Node) map[*xmltree.Node]error {
	var recs []dnsx.Rec
	var nodes []*xmltree.Node
	for _, c := range local.Elements() {
		switch c.Name {
		case "record":
			recs = append(recs, dnsx.Rec{Name: attr(c, "name"), Type: attr(c, "type")})
		case "redirect", "dynhost":
			recs = append(recs, dnsx.Rec{Name: attr(c, "name"), Type: "A"})
		default:
			continue
		}
		nodes = append(nodes, c)
	}
	out := map[*xmltree.Node]error{}
	for i, err := range dnsx.CNAMEConflicts(recs) {
		if err != nil {
			out[nodes[i]] = err
		}
	}
	return out
}

// check refuses an action OVH would refuse or that would break the zone
// (DNS spec §7.3).
func check(a adapter.Action, local, base *xmltree.Node, cn map[*xmltree.Node]error) error {
	name, ln, bn := elems(a, local, base)
	if a.Target == "" && a.Group == "" {
		return fmt.Errorf("gfs cannot %s zones; they come with their domain", a.Verb)
	}
	if a.Verb == "delete" {
		return nil
	}
	switch name {
	case "soa":
		if ln == nil {
			return errors.New("the SOA cannot be removed")
		}
		for _, k := range []string{"ttl", "refresh", "expire", "nx-domain-ttl"} {
			if err := checkNum(k, attr(ln, k)); err != nil {
				return err
			}
		}
		if attr(ln, "email") == "" {
			return errors.New("soa needs an email")
		}
		return nil
	case "dnssec":
		if ln == nil {
			return errors.New("<dnssec> cannot be removed; set it to disabled")
		}
		if s := textOf(bn); strings.HasSuffix(s, "InProgress") {
			return fmt.Errorf("DNSSEC is %s; wait until OVH finishes, then pull", s)
		}
		if v := textOf(ln); v != "enabled" && v != "disabled" {
			return fmt.Errorf("dnssec %q: want enabled or disabled", v)
		}
		return nil
	}
	if ln == nil {
		return fmt.Errorf("%s: element not found", a.Target)
	}
	if err := checkLossy(textOf(ln), attr(ln, "title"), attr(ln, "keywords"), attr(ln, "description")); err != nil {
		return err
	}
	if n := attr(ln, "name"); n != "" || name != "dynhost-login" {
		if err := dnsx.CheckName(n); err != nil {
			return err
		}
	}
	switch name {
	case "record":
		typ := attr(ln, "type")
		if !recordTypes[typ] {
			return fmt.Errorf("type %q is not an OVH record type", typ)
		}
		if err := checkNum("ttl", attr(ln, "ttl")); err != nil {
			return err
		}
		if err := checkValue(typ, textOf(ln)); err != nil {
			return err
		}
		return cn[ln]
	case "redirect":
		if !redirectTypes[attr(ln, "type")] {
			return fmt.Errorf("redirect type %q: want visible, visiblePermanent or invisible", attr(ln, "type"))
		}
		if textOf(ln) == "" {
			return errors.New("redirect without a target")
		}
		return cn[ln]
	case "dynhost":
		if err := dnsx.CheckIPv4(textOf(ln)); err != nil {
			return err
		}
		return cn[ln]
	case "dynhost-login":
		if a.Verb == "create" {
			if s := attr(ln, "suffix"); !sufRe.MatchString(s) {
				return fmt.Errorf("a new dynhost-login needs suffix=\"…\" (letters, digits, -); the login becomes <zone>-<suffix>")
			}
			if attr(ln, "name") == "" {
				return errors.New("a new dynhost-login needs name=\"…\" (the subdomain it may update)")
			}
		}
		return nil
	}
	return fmt.Errorf("unknown element <%s>", name)
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	local, base := roots(req)
	cn := map[*xmltree.Node]error{}
	if local != nil {
		cn = cnameErrs(local)
	}
	out := make([]adapter.Result, len(req.Actions))
	for i, a := range req.Actions {
		describe(&a, local, base)
		out[i] = adapter.Result{Action: a, Detail: a.Detail, Err: check(a, local, base, cn)}
	}
	return out
}

func roots(req adapter.ApplyRequest) (local, base *xmltree.Node) {
	if req.Local != nil {
		local = req.Local.Root
	}
	if req.Base != nil {
		base = req.Base.Root
	}
	return
}

func phase(a adapter.Action) int {
	switch a.Verb {
	case "delete":
		return 0
	case "update":
		return 1
	}
	return 2
}

// Apply runs one zone's actions: deletes, then updates, then creates, then
// one refresh (DNS spec §7.2). Results come back in action order, the
// refresh last when it failed or anything was sent.
func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	local, base := roots(req)
	z := req.Local.ID
	if z == "" && req.Base != nil {
		z = req.Base.ID
	}
	out := s.Check(ctx, req)
	sent := false
	for p := range 3 {
		for i := range out {
			if out[i].Err != nil || phase(out[i].Action) != p {
				continue
			}
			did, err := s.apply(ctx, z, out[i].Action, local, base, req.Secret)
			sent = sent || did
			if err != nil {
				out[i].Err = err
				out[i].Partial = did
				var ae *APIError
				if errors.As(err, &ae) {
					out[i].Code = strconv.Itoa(ae.Status)
				}
			}
		}
	}
	if sent {
		res := adapter.Result{Action: adapter.Action{Verb: "refresh", Class: "dns", Detail: "refresh zone " + z}}
		if err := s.c.Do(ctx, http.MethodPost, zonePath(z, "refresh"), nil, nil); err != nil {
			res.Err = fmt.Errorf("refresh: %w (the changes are saved; they reach the name servers on the next refresh)", err)
		}
		out = append(out, res)
	}
	return out
}

func ttlValue(n *xmltree.Node) int64 {
	v, _ := strconv.ParseInt(attr(n, "ttl"), 10, 64)
	return v
}

func optional(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// apply sends one action; did reports whether the zone changed, even when
// err is set (the delete half of a delete-and-create).
func (s *session) apply(ctx context.Context, z string, a adapter.Action, local, base *xmltree.Node, secret func(string) (string, error)) (did bool, err error) {
	name, ln, bn := elems(a, local, base)
	do := func(method, path string, body any) error { return s.c.Do(ctx, method, path, body, nil) }
	id := attr(bn, "id")
	switch name {
	case "soa":
		return true, do(http.MethodPut, zonePath(z, "soa"), map[string]any{"ttl": num(ln, "ttl"), "refresh": num(ln, "refresh"),
			"expire": num(ln, "expire"), "nxDomainTtl": num(ln, "nx-domain-ttl"), "email": attr(ln, "email"),
			"server": attr(bn, "server"), "serial": num(bn, "serial")})
	case "dnssec":
		if textOf(ln) == "enabled" {
			return true, do(http.MethodPost, zonePath(z, "dnssec"), nil)
		}
		return true, do(http.MethodDelete, zonePath(z, "dnssec"), nil)
	}
	createRecord := func() error {
		return do(http.MethodPost, zonePath(z, "record"), map[string]any{"fieldType": attr(ln, "type"),
			"subDomain": dnsx.APIName(attr(ln, "name")), "target": textOf(ln), "ttl": ttlValue(ln)})
	}
	createRedirect := func() error {
		body := map[string]any{"subDomain": dnsx.APIName(attr(ln, "name")), "target": textOf(ln), "type": attr(ln, "type")}
		optional(body, "title", attr(ln, "title"))
		optional(body, "keywords", attr(ln, "keywords"))
		optional(body, "description", attr(ln, "description"))
		return do(http.MethodPost, zonePath(z, "redirection"), body)
	}
	replace := func(kind string, create func() error) (bool, error) {
		if err := do(http.MethodDelete, zonePath(z, kind, id), nil); err != nil {
			return false, err
		}
		if err := create(); err != nil {
			return true, fmt.Errorf("deleted, create failed: %w", err)
		}
		return true, nil
	}
	switch name + " " + a.Verb {
	case "record create":
		return true, createRecord()
	case "record delete":
		return true, do(http.MethodDelete, zonePath(z, "record", id), nil)
	case "record update":
		if attr(bn, "type") != attr(ln, "type") {
			return replace("record", createRecord)
		}
		return true, do(http.MethodPut, zonePath(z, "record", id), map[string]any{"subDomain": dnsx.APIName(attr(ln, "name")),
			"target": textOf(ln), "ttl": ttlValue(ln)})
	case "redirect create":
		return true, createRedirect()
	case "redirect delete":
		return true, do(http.MethodDelete, zonePath(z, "redirection", id), nil)
	case "redirect update":
		if attr(bn, "name") != attr(ln, "name") {
			return replace("redirection", createRedirect)
		}
		body := map[string]any{"target": textOf(ln), "type": attr(ln, "type")}
		optional(body, "title", attr(ln, "title"))
		optional(body, "keywords", attr(ln, "keywords"))
		optional(body, "description", attr(ln, "description"))
		return true, do(http.MethodPut, zonePath(z, "redirection", id), body)
	case "dynhost create":
		return true, do(http.MethodPost, zonePath(z, "dynHost/record"), map[string]any{"subDomain": dnsx.APIName(attr(ln, "name")), "ip": textOf(ln)})
	case "dynhost update":
		return true, do(http.MethodPut, zonePath(z, "dynHost/record", id), map[string]any{"subDomain": dnsx.APIName(attr(ln, "name")), "ip": textOf(ln)})
	case "dynhost delete":
		return true, do(http.MethodDelete, zonePath(z, "dynHost/record", id), nil)
	case "dynhost-login create":
		if secret == nil {
			return false, errors.New("a new dynhost-login needs a password: run gfs commit in a terminal")
		}
		pw, err := secret(fmt.Sprintf("Password for DynHost login %s-%s: ", z, attr(ln, "suffix")))
		if err != nil {
			return false, err
		}
		return true, do(http.MethodPost, zonePath(z, "dynHost/login"), map[string]any{"loginSuffix": attr(ln, "suffix"),
			"subDomain": dnsx.APIName(attr(ln, "name")), "password": pw})
	case "dynhost-login update":
		return true, do(http.MethodPut, zonePath(z, "dynHost/login", url.PathEscape(attr(bn, "login"))), map[string]any{"subDomain": dnsx.APIName(attr(ln, "name"))})
	case "dynhost-login delete":
		return true, do(http.MethodDelete, zonePath(z, "dynHost/login", url.PathEscape(attr(bn, "login"))), nil)
	}
	return false, fmt.Errorf("%s %s is not supported", a.Verb, a.Target)
}

func num(n *xmltree.Node, k string) int64 {
	v, _ := strconv.ParseInt(attr(n, k), 10, 64)
	return v
}
