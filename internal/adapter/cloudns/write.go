package cloudns

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const (
	warnNS       = "changes the zone's own NS records: a mistake makes the whole zone unreachable"
	warnSOA      = "SOA timers decide how long resolvers keep stale answers; check the values before sending"
	warnDNSOff   = "if the registrar publishes a DS record for this domain, remove it first or the domain stops resolving"
	warnDNSOn    = "once DNSSEC is active, publish the <ds> records shown in this file at the registrar"
	warnOff      = "an inactive zone is not served: every name in it stops resolving"
	warnFailover = "a type change gives the record a new id, and ClouDNS removes its failover with the old one: add the failover again after this commit"
)

// geoTypes may carry a GeoDNS location.
var geoTypes = []string{"A", "AAAA", "CNAME", "NAPTR", "SRV", "ALIAS"}

// ownAttrs are record attributes gfs maps itself; others are type-specific
// fields passed to add-record/mod-record by name (caa-flag → caa_flag).
var ownAttrs = map[string]bool{"id": true, "name": true, "type": true, "ttl": true, "geo": true, "status": true}

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return n.TextContent()
}

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

func child(n *xmltree.Node, name string) *xmltree.Node {
	if n == nil || name == "" {
		return nil
	}
	return n.Child(name)
}

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

func extras(n *xmltree.Node) []xmltree.Attr {
	var out []xmltree.Attr
	for _, a := range n.Attrs {
		if !ownAttrs[a.Name] {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
		if g := attr(n, "geo"); g != "" {
			s += " geo " + g
		}
		if attr(n, "status") == "0" {
			s += " (inactive)"
		}
		for _, a := range extras(n) {
			s += " " + a.Name + " " + a.Value
		}
		return s
	case "mail-forward":
		box := attr(n, "box")
		if h := attr(n, "host"); h != "" {
			box += "@" + h
		}
		return fmt.Sprintf("mail-forward %s → %s", box, attr(n, "destination"))
	}
	return name
}

func failovers(root *xmltree.Node) map[string]*xmltree.Node {
	out := map[string]*xmltree.Node{}
	if root == nil {
		return out
	}
	for _, f := range root.ChildrenNamed("failover") {
		out[attr(f, "record")] = f
	}
	return out
}

type foChange struct {
	verb, record string // activate, modify, deactivate
	node         *xmltree.Node
}

// foText is a failover as text with its attributes in name order: the
// file's order and the API's (map) order must compare equal.
func foText(n *xmltree.Node) string {
	c := n.Clone()
	sort.Slice(c.Attrs, func(i, j int) bool { return c.Attrs[i].Name < c.Attrs[j].Name })
	return xmltree.Print(c, 0)
}

// foPlan compares the base and local failover sets by record id.
func foPlan(local, base *xmltree.Node) []foChange {
	lf, bf := failovers(local), failovers(base)
	var out []foChange
	ids := map[string]bool{}
	for id := range lf {
		ids[id] = true
	}
	for id := range bf {
		ids[id] = true
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		l, b := lf[id], bf[id]
		switch {
		case b == nil:
			out = append(out, foChange{"activate", id, l})
		case l == nil:
			out = append(out, foChange{"deactivate", id, b})
		case foText(l) != foText(b):
			out = append(out, foChange{"modify", id, l})
		}
	}
	return out
}

func describe(a *adapter.Action, local, base *xmltree.Node) {
	a.Class = "dns"
	name, ln, bn := elems(*a, local, base)
	switch {
	case a.Target == "" && a.Group == "":
		a.Detail = a.Verb + " zone (not supported: add or remove zones in ClouDNS)"
	case name == "soa":
		a.Detail = "update soa"
		for _, k := range []string{"primary", "admin", "refresh", "retry", "expire", "ttl"} {
			if attr(bn, k) != attr(ln, k) {
				a.Detail += fmt.Sprintf(" %s %s → %s", k, attr(bn, k), attr(ln, k))
			}
		}
		a.Warning = warnSOA
	case name == "dnssec":
		a.Detail = "dnssec " + attr(bn, "status") + " → " + attr(ln, "status")
		switch attr(ln, "status") {
		case "disabled":
			a.Warning = warnDNSOff
		case "enabled":
			a.Warning = warnDNSOn
		}
	case name == "active":
		a.Detail = "zone active " + textOf(bn) + " → " + textOf(ln)
		if textOf(ln) == "false" {
			a.Warning = warnOff
		}
	case name == "failover":
		var parts []string
		for _, c := range foPlan(local, base) {
			parts = append(parts, c.verb+" failover on record "+c.record)
		}
		a.Detail = strings.Join(parts, ", ")
	case a.Verb == "create":
		a.Detail = "create " + summary(name, ln)
	case a.Verb == "delete":
		a.Detail = "delete " + summary(name, bn)
	case name == "record" && bn != nil && ln != nil && textOf(bn) != textOf(ln) &&
		xmltree.Print(withText(ln.Clone(), "-"), 0) == xmltree.Print(withText(bn.Clone(), "-"), 0):
		a.Detail = fmt.Sprintf("update record %s %s %s → %s", attr(ln, "name"), attr(ln, "type"), textOf(bn), textOf(ln))
	default:
		a.Detail = "update " + summary(name, bn) + " → " + summary(name, ln)
	}
	if name == "record" && (isApexNS(ln) || isApexNS(bn)) {
		a.Warning = warnNS
	}
	if name == "record" && a.Verb == "update" && bn != nil && ln != nil && attr(bn, "type") != attr(ln, "type") {
		if _, ok := failovers(base)[attr(bn, "id")]; ok {
			a.Warning = warnFailover
		}
	}
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

// limits loads what the account allows: TTLs and record types.
func (s *session) limits(ctx context.Context) error {
	if s.ttls != nil {
		return nil
	}
	if err := s.c.Do(ctx, "get-available-ttl", nil, &s.ttls); err != nil {
		return fmt.Errorf("allowed TTLs: %w", err)
	}
	if err := s.c.Do(ctx, "get-available-record-types", url.Values{"zone-type": {"domain"}}, &s.types); err != nil {
		return fmt.Errorf("allowed record types: %w", err)
	}
	return nil
}

var (
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	boxRe   = regexp.MustCompile(`^[a-z0-9._+-]+$`)
)

func (s *session) checkRecord(ln *xmltree.Node, geodns bool, cn map[*xmltree.Node]error) error {
	if err := dnsx.CheckName(attr(ln, "name")); err != nil {
		return err
	}
	typ := attr(ln, "type")
	if !slices.Contains(s.types, typ) {
		return fmt.Errorf("type %q is not a ClouDNS record type", typ)
	}
	ttl, err := strconv.Atoi(attr(ln, "ttl"))
	if err != nil || !slices.Contains(s.ttls, ttl) {
		var allowed []string
		for _, t := range s.ttls {
			allowed = append(allowed, strconv.Itoa(t))
		}
		if attr(ln, "ttl") == "" {
			return fmt.Errorf("a ClouDNS record needs ttl (one of %s)", strings.Join(allowed, ", "))
		}
		return fmt.Errorf("ttl %s is not one ClouDNS allows (%s)", attr(ln, "ttl"), strings.Join(allowed, ", "))
	}
	for _, v := range append([]string{textOf(ln)}, attrValues(ln)...) {
		if dnsx.Lossy(v) {
			return errors.New("the value holds characters XML cannot hold (shown as �); change it in ClouDNS")
		}
	}
	if st := attr(ln, "status"); st != "" && st != "0" && st != "1" {
		return fmt.Errorf("status %q: want 0 (inactive) or 1", st)
	}
	if g := attr(ln, "geo"); g != "" {
		switch {
		case !geodns:
			return errors.New("geo is set but this is not a GeoDNS zone")
		case !slices.Contains(geoTypes, typ):
			return fmt.Errorf("geo only on %s records", strings.Join(geoTypes, ", "))
		case s.geo == nil || s.geo.idOf[g] == "":
			return fmt.Errorf("geo %q is not in .geodns.xml", g)
		}
	}
	v := textOf(ln)
	switch typ {
	case "A":
		err = dnsx.CheckIPv4(v)
	case "AAAA":
		err = dnsx.CheckIPv6(v)
	case "CNAME", "NS", "PTR", "ALIAS", "DNAME":
		err = dnsx.CheckHost(v)
	case "MX":
		if _, e := strconv.Atoi(attr(ln, "priority")); e != nil {
			return errors.New("MX needs priority=\"<number>\"")
		}
		err = dnsx.CheckHost(v)
	case "SRV":
		for _, k := range []string{"priority", "weight", "port"} {
			if _, e := strconv.Atoi(attr(ln, k)); e != nil {
				return errors.New("SRV needs priority, weight and port (numbers)")
			}
		}
		err = dnsx.CheckHost(v)
	case "WR":
		if rt := attr(ln, "redirect-type"); attr(ln, "frame") != "1" && rt != "301" && rt != "302" {
			return errors.New("a WR record without frame=\"1\" needs redirect-type 301 or 302")
		}
		if v == "" {
			err = errors.New("WR record without a target URL")
		}
	case "CAA":
		if attr(ln, "caa-flag") == "" || attr(ln, "caa-type") == "" {
			return errors.New("CAA needs caa-flag and caa-type")
		}
	}
	if err != nil {
		return err
	}
	return cn[ln]
}

func attrValues(n *xmltree.Node) []string {
	var out []string
	for _, a := range n.Attrs {
		out = append(out, a.Value)
	}
	return out
}

// cnameErrs applies the CNAME rules per name and GeoDNS location: in a
// GeoDNS zone the same name may hold a CNAME per location.
func cnameErrs(local *xmltree.Node, geodns bool) map[*xmltree.Node]error {
	var recs []dnsx.Rec
	nodes := local.ChildrenNamed("record")
	for _, c := range nodes {
		n := attr(c, "name")
		if geodns && slices.Contains(geoTypes, attr(c, "type")) {
			n += "|" + attr(c, "geo")
		}
		recs = append(recs, dnsx.Rec{Name: n, Type: attr(c, "type")})
	}
	out := map[*xmltree.Node]error{}
	for i, err := range dnsx.CNAMEConflicts(recs) {
		if err != nil {
			out[nodes[i]] = errors.New(strings.Replace(err.Error(), "|"+attr(nodes[i], "geo"), "", 1))
		}
	}
	return out
}

func (s *session) check(ctx context.Context, a adapter.Action, local, base *xmltree.Node, cn map[*xmltree.Node]error) error {
	name, ln, bn := elems(a, local, base)
	if a.Target == "" && a.Group == "" {
		return fmt.Errorf("gfs cannot %s zones; add or remove them in ClouDNS", a.Verb)
	}
	geodns := attr(local, "kind") == "geodns"
	if a.Verb == "delete" {
		return nil
	}
	switch name {
	case "soa":
		if ln == nil {
			return errors.New("the SOA cannot be removed")
		}
		for _, k := range []string{"refresh", "retry", "expire", "ttl"} {
			if _, err := strconv.Atoi(attr(ln, k)); err != nil {
				return fmt.Errorf("soa %s %q is not a number of seconds", k, attr(ln, k))
			}
		}
		if err := dnsx.CheckHost(attr(ln, "primary")); err != nil {
			return fmt.Errorf("soa primary: %w", err)
		}
		if !emailRe.MatchString(attr(ln, "admin")) {
			return fmt.Errorf("soa admin %q is not an email address", attr(ln, "admin"))
		}
		return nil
	case "dnssec":
		if ln == nil {
			return errors.New(`<dnssec> cannot be removed; set status="disabled"`)
		}
		if st := attr(ln, "status"); st != "enabled" && st != "disabled" {
			return fmt.Errorf("dnssec status %q: want enabled or disabled", st)
		}
		if attr(ln, "status") == attr(bn, "status") && xmltree.Print(dsOnly(ln), 0) != xmltree.Print(dsOnly(bn), 0) {
			return errors.New("DS records are read-only: they show what to publish at the registrar")
		}
		return nil
	case "active":
		if v := textOf(ln); v != "true" && v != "false" {
			return fmt.Errorf("active %q: want true or false", v)
		}
		return nil
	case "failover":
		records := map[string]*xmltree.Node{}
		for _, r := range local.ChildrenNamed("record") {
			if id := attr(r, "id"); id != "" {
				records[id] = r
			}
		}
		for _, c := range foPlan(local, base) {
			if c.verb == "deactivate" {
				continue
			}
			r := records[c.record]
			if r == nil {
				return fmt.Errorf("failover: record %s is not a record of this zone (commit a new record first, then add its failover)", c.record)
			}
			if t := attr(r, "type"); t != "A" && t != "AAAA" && t != "CNAME" {
				return fmt.Errorf("failover on record %s: ClouDNS watches A, AAAA and CNAME records, not %s", c.record, t)
			}
			if attr(c.node, "check-type") == "" {
				return fmt.Errorf("failover on record %s needs check-type", c.record)
			}
			for _, b := range c.node.ChildrenNamed("backup") {
				if err := dnsx.CheckIPv4(textOf(b)); err != nil && dnsx.CheckIPv6(textOf(b)) != nil {
					return fmt.Errorf("failover backup: %w", err)
				}
			}
		}
		return nil
	case "mail-forward":
		if ln == nil {
			return fmt.Errorf("%s: element not found", a.Target)
		}
		if !boxRe.MatchString(attr(ln, "box")) {
			return fmt.Errorf("mail-forward box %q: want the part before @", attr(ln, "box"))
		}
		if !emailRe.MatchString(attr(ln, "destination")) {
			return fmt.Errorf("mail-forward destination %q is not an email address", attr(ln, "destination"))
		}
		return nil
	case "record":
		if ln == nil {
			return fmt.Errorf("%s: element not found", a.Target)
		}
		if err := s.limits(ctx); err != nil {
			return err
		}
		if geodns {
			if _, err := s.geoTable(ctx); err != nil {
				return err
			}
		}
		return s.checkRecord(ln, geodns, cn)
	}
	return fmt.Errorf("unknown element <%s>", name)
}

func dsOnly(n *xmltree.Node) *xmltree.Node {
	out := &xmltree.Node{Kind: xmltree.Element, Name: "ds"}
	if n != nil {
		out.Children = n.ChildrenNamed("ds")
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

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	local, base := roots(req)
	out := make([]adapter.Result, len(req.Actions))
	if req.Local != nil && req.Local.ID == geodnsID {
		for i, a := range req.Actions {
			out[i] = adapter.Result{Action: a, Err: errors.New(".geodns.xml is read-only: it lists the GeoDNS locations ClouDNS offers")}
		}
		return out
	}
	cn := map[*xmltree.Node]error{}
	if local != nil {
		cn = cnameErrs(local, attr(local, "kind") == "geodns")
	}
	for i, a := range req.Actions {
		describe(&a, local, base)
		out[i] = adapter.Result{Action: a, Detail: a.Detail, Err: s.check(ctx, a, local, base, cn)}
	}
	return out
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

// Apply runs one zone's actions: deletes, then updates, then creates (DNS
// spec §7.2); results come back in action order.
func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	local, base := roots(req)
	out := s.Check(ctx, req)
	z := req.Local.ID
	for p := range 3 {
		for i := range out {
			if out[i].Err != nil || phase(out[i].Action) != p {
				continue
			}
			detail, did, err := s.apply(ctx, z, out[i].Action, local, base)
			if detail != "" {
				out[i].Detail = detail
			}
			if err != nil {
				out[i].Err, out[i].Partial = err, did
			}
		}
	}
	return out
}

// recordParams are the add-record/mod-record parameters for a record:
// the whole record, so a field removed locally is reset, not kept (no
// status is active, no geo is the default location).
func (s *session) recordParams(z string, ln *xmltree.Node, geodns bool) url.Values {
	v := url.Values{"domain-name": {z}, "host": {dnsx.APIName(attr(ln, "name"))}, "record": {textOf(ln)}, "ttl": {attr(ln, "ttl")}}
	st := attr(ln, "status")
	if st == "" {
		st = "1"
	}
	v.Set("status", st)
	if geodns && slices.Contains(geoTypes, attr(ln, "type")) && s.geo != nil {
		g := attr(ln, "geo")
		if g == "" {
			g = "DEFAULT"
		}
		v.Set("geodns-location", s.geo.idOf[g])
	}
	for _, a := range extras(ln) {
		v.Set(strings.ReplaceAll(a.Name, "-", "_"), a.Value)
	}
	return v
}

func failoverParams(z, record string, n *xmltree.Node) url.Values {
	v := url.Values{"domain-name": {z}, "record-id": {record}}
	for _, a := range n.Attrs {
		if a.Name != "record" {
			v.Set(strings.ReplaceAll(a.Name, "-", "_"), a.Value)
		}
	}
	for i, b := range n.ChildrenNamed("backup") {
		v.Set(fmt.Sprintf("backup_ip_%d", i+1), textOf(b))
	}
	return v
}

// apply sends one action. did reports a change made even when err is set
// (the delete half of a type change).
func (s *session) apply(ctx context.Context, z string, a adapter.Action, local, base *xmltree.Node) (detail string, did bool, err error) {
	name, ln, bn := elems(a, local, base)
	geodns := attr(local, "kind") == "geodns"
	zone := url.Values{"domain-name": {z}}
	do := func(action string, v url.Values) error { return s.c.Do(ctx, action, v, nil) }
	addRecord := func() (string, error) {
		v := s.recordParams(z, ln, geodns)
		v.Set("record-type", attr(ln, "type"))
		var resp struct {
			Data struct {
				ID flex `json:"id"`
			} `json:"data"`
		}
		if err := s.c.Do(ctx, "add-record", v, &resp); err != nil {
			return "", err
		}
		return "id=" + string(resp.Data.ID), nil
	}
	with := func(k, val string) url.Values {
		v := url.Values{"domain-name": {z}}
		v.Set(k, val)
		return v
	}
	switch name {
	case "soa":
		v := url.Values{"domain-name": {z}, "primary-ns": {attr(ln, "primary")}, "admin-mail": {attr(ln, "admin")},
			"refresh": {attr(ln, "refresh")}, "retry": {attr(ln, "retry")}, "expire": {attr(ln, "expire")}, "default-ttl": {attr(ln, "ttl")}}
		return "", true, do("modify-soa", v)
	case "dnssec":
		if attr(ln, "status") == attr(bn, "status") {
			return "", false, nil
		}
		if attr(ln, "status") == "enabled" {
			return "", true, do("activate-dnssec", zone)
		}
		return "", true, do("deactivate-dnssec", zone)
	case "active":
		st := "0"
		if textOf(ln) == "true" {
			st = "1"
		}
		return "", true, do("change-status", with("status", st))
	case "failover":
		deleted := map[string]bool{}
		for _, r := range base.ChildrenNamed("record") {
			if id := attr(r, "id"); validate.FindSub(local, "record", "id", id) == nil {
				deleted[id] = true
			}
		}
		for _, c := range foPlan(local, base) {
			var err error
			switch c.verb {
			case "deactivate":
				if deleted[c.record] {
					continue // went with its record
				}
				err = do("failover-deactivate", with("record-id", c.record))
			case "activate":
				err = do("failover-activate", failoverParams(z, c.record, c.node))
			case "modify":
				err = do("failover-modify", failoverParams(z, c.record, c.node))
			}
			if err != nil {
				return "", did, fmt.Errorf("%s failover on record %s: %w", c.verb, c.record, err)
			}
			did = true
		}
		return "", did, nil
	}
	id := attr(bn, "id")
	switch name + " " + a.Verb {
	case "record create":
		d, err := addRecord()
		return d, err == nil, err
	case "record delete":
		return "", true, do("delete-record", with("record-id", id))
	case "record update":
		if attr(bn, "type") != attr(ln, "type") {
			if err := do("delete-record", with("record-id", id)); err != nil {
				return "", false, err
			}
			d, err := addRecord()
			if err != nil {
				return "", true, fmt.Errorf("deleted, create failed: %w", err)
			}
			return d, true, nil
		}
		v := s.recordParams(z, ln, geodns)
		v.Set("record-id", id)
		return "", true, do("mod-record", v)
	case "mail-forward create":
		return "", true, do("add-mail-forward", url.Values{"domain-name": {z}, "box": {attr(ln, "box")}, "host": {attr(ln, "host")},
			"destination": {attr(ln, "destination")}})
	case "mail-forward update":
		return "", true, do("modify-mail-forward", url.Values{"domain-name": {z}, "mail-forward-id": {id}, "box": {attr(ln, "box")},
			"host": {attr(ln, "host")}, "destination": {attr(ln, "destination")}})
	case "mail-forward delete":
		return "", true, do("delete-mail-forward", with("mail-forward-id", id))
	}
	return "", false, fmt.Errorf("%s %s is not supported", a.Verb, a.Target)
}
