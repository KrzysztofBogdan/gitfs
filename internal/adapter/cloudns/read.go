package cloudns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const rows = 100

type apiZone struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Kind   string `json:"zone"`
	Status string `json:"status"`
	Serial string `json:"serial"`
}

// flex reads a JSON string or number as text (ClouDNS mixes both).
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flex(s)
		return nil
	}
	*f = flex(b)
	return nil
}

// zones lists the API user's zones inside the selection, paged.
func (s *session) zones(ctx context.Context) ([]apiZone, error) {
	var out []apiZone
	for page := 1; ; page++ {
		var zs []apiZone
		if err := s.c.Do(ctx, "list-zones", url.Values{"page": {strconv.Itoa(page)}, "rows-per-page": {strconv.Itoa(rows)}}, &zs); err != nil {
			return nil, err
		}
		for _, z := range zs {
			if s.t.sel.wants(z.Name) {
				out = append(out, z)
			}
		}
		if len(zs) < rows {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// keyed decodes a ClouDNS list: an object keyed by id, or [] / {} when empty.
func keyed(data []byte) (map[string]map[string]flex, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] == '[' {
		return nil, nil
	}
	var m map[string]map[string]flex
	return m, json.Unmarshal(data, &m)
}

// records reads every record of z, in pages of 100.
func (s *session) records(ctx context.Context, z string) ([]map[string]flex, error) {
	var out []map[string]flex
	for page := 1; ; page++ {
		data, err := s.c.Raw(ctx, "records", url.Values{"domain-name": {z}, "page": {strconv.Itoa(page)}, "rows-per-page": {strconv.Itoa(rows)}})
		if err != nil {
			return nil, err
		}
		m, err := keyed(data)
		if err != nil {
			return nil, fmt.Errorf("records: %w", err)
		}
		for _, r := range m {
			out = append(out, r)
		}
		if len(m) < rows {
			return out, nil
		}
	}
}

type location struct {
	ID, Code, Name, Parent string
}

type geoTable struct {
	list           []location
	codeOf, idOf   map[string]string // id -> code, code -> id
	parentOf, kind map[string]string
}

func (s *session) geoTable(ctx context.Context) (*geoTable, error) {
	if s.geo != nil {
		return s.geo, nil
	}
	var ls []struct {
		ID       flex `json:"id"`
		Name     string
		ParentID flex `json:"parent_id"`
		Code     string
	}
	zone := s.geoZone
	if zone == "" {
		zs, err := s.zones(ctx)
		if err != nil {
			return nil, err
		}
		for _, z := range zs {
			if z.Kind == "geodns" {
				zone = z.Name
				break
			}
		}
	}
	if err := s.c.Do(ctx, "get-geodns-locations", url.Values{"domain-name": {zone}}, &ls); err != nil {
		return nil, fmt.Errorf("GeoDNS locations: %w", err)
	}
	g := &geoTable{codeOf: map[string]string{}, idOf: map[string]string{}}
	for _, l := range ls {
		g.list = append(g.list, location{ID: string(l.ID), Code: l.Code, Name: l.Name, Parent: string(l.ParentID)})
		g.codeOf[string(l.ID)], g.idOf[l.Code] = l.Code, string(l.ID)
	}
	s.geo = g
	return g, nil
}

// known record fields, mapped to attributes by name; everything else in a
// record (type-specific fields) becomes an attribute named after the field.
var known = map[string]bool{"id": true, "type": true, "host": true, "record": true, "ttl": true, "status": true, "failover": true,
	"geodns-location": true, "geodns-location-name": true, "geodns-location-code": true, "dynamicurl_status": true}

func attrName(field string) string { return strings.ReplaceAll(strings.ToLower(field), "_", "-") }

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], dnsx.XMLText(attrs[i+1]))
		}
	}
	return n
}

func withText(n *xmltree.Node, text string) *xmltree.Node {
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: dnsx.XMLText(text)}}
	}
	return n
}

func attr(n *xmltree.Node, k string) string {
	if n == nil {
		return ""
	}
	v, _ := n.Attr(k)
	return v
}

var backupRe = regexp.MustCompile(`^backup_ip_(\d+)$`)

// readZone is the zone file's content (DNS spec §5.2).
func (s *session) readZone(ctx context.Context, z string) (*xmltree.Node, error) {
	wrap := func(err error) error { return fmt.Errorf("zone %s: %w", z, err) }
	var info struct {
		Name, Type, Zone, Status string
	}
	if err := s.c.Do(ctx, "get-zone-info", url.Values{"domain-name": {z}}, &info); err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return nil, fmt.Errorf("zone %s: %w: %w", z, adapter.ErrNotFound, err)
		}
		return nil, wrap(err)
	}
	active := "false"
	if info.Status == "1" {
		active = "true"
	}
	root := el("zone", "name", z, "type", info.Type, "kind", info.Zone)
	var soa map[string]flex
	if err := s.c.Do(ctx, "soa-details", url.Values{"domain-name": {z}}, &soa); err != nil {
		return nil, wrap(err)
	}
	root.Children = append(root.Children, el("soa", "primary", string(soa["primaryNS"]), "admin", string(soa["adminMail"]),
		"refresh", string(soa["refresh"]), "retry", string(soa["retry"]), "expire", string(soa["expire"]),
		"ttl", string(soa["defaultTTL"]), "serial", string(soa["serialNumber"])))
	var avail flex
	if err := s.c.Do(ctx, "is-dnssec-available", url.Values{"domain-name": {z}}, &avail); err != nil {
		return nil, wrap(err)
	}
	if avail == "1" || avail == "true" {
		var ds struct {
			Status    flex `json:"status"`
			DSRecords []struct {
				Digest     string `json:"digest"`
				KeyTag     flex   `json:"key_tag"`
				Algorithm  flex   `json:"algorithm"`
				DigestType flex   `json:"digest_type"`
			} `json:"ds_records"`
		}
		if err := s.c.Do(ctx, "get-dnssec-ds-records", url.Values{"domain-name": {z}}, &ds); err != nil {
			return nil, wrap(err)
		}
		d := el("dnssec", "status", "disabled")
		if ds.Status == "1" {
			d.SetAttr("status", "enabled")
			for _, r := range ds.DSRecords {
				d.Children = append(d.Children, withText(el("ds", "key-tag", string(r.KeyTag), "algorithm", string(r.Algorithm),
					"digest-type", string(r.DigestType)), r.Digest))
			}
		}
		root.Children = append(root.Children, d)
	}
	root.Children = append(root.Children, withText(el("active"), active))
	recs, err := s.records(ctx, z)
	if err != nil {
		return nil, wrap(err)
	}
	var geo *geoTable
	if info.Zone == "geodns" {
		if s.geoZone == "" {
			s.geoZone = z
		}
		if geo, err = s.geoTable(ctx); err != nil {
			return nil, wrap(err)
		}
	}
	var failover []string
	for _, r := range recs {
		n := withText(el("record", "id", string(r["id"]), "name", dnsx.FileName(string(r["host"])), "type", string(r["type"]),
			"ttl", string(r["ttl"])), string(r["record"]))
		if string(r["status"]) == "0" {
			n.SetAttr("status", "0")
		}
		if g := string(r["geodns-location"]); geo != nil && g != "" && g != geo.idOf["DEFAULT"] {
			code := geo.codeOf[g]
			if code == "" {
				code = string(r["geodns-location-code"])
			}
			n.SetAttr("geo", code)
		}
		for k, v := range r {
			if !known[k] && v != "" {
				n.SetAttr(attrName(k), dnsx.XMLText(string(v)))
			}
		}
		if string(r["failover"]) == "1" {
			failover = append(failover, string(r["id"]))
		}
		root.Children = append(root.Children, n)
	}
	sort.Strings(failover)
	for _, id := range failover {
		var st map[string]flex
		if err := s.c.Do(ctx, "failover-settings", url.Values{"domain-name": {z}, "record-id": {id}}, &st); err != nil {
			return nil, wrap(fmt.Errorf("failover of record %s: %w", id, err))
		}
		root.Children = append(root.Children, failoverNode(id, st))
	}
	mf, err := s.c.Raw(ctx, "mail-forwards", url.Values{"domain-name": {z}})
	if err != nil {
		return nil, wrap(err)
	}
	fwd, err := keyed(mf)
	if err != nil {
		return nil, wrap(fmt.Errorf("mail forwards: %w", err))
	}
	for _, m := range fwd {
		host := string(m["host"])
		root.Children = append(root.Children, el("mail-forward", "id", string(m["id"]), "box", string(m["box"]), "host", host,
			"destination", string(m["destination"])))
	}
	return root, nil
}

// failoverNode is <failover record="id"> with every setting as an attribute
// and backup_ip_N as <backup> children in order.
func failoverNode(id string, st map[string]flex) *xmltree.Node {
	n := el("failover", "record", id)
	type backup struct {
		i  int
		ip string
	}
	var bs []backup
	for k, v := range st {
		if m := backupRe.FindStringSubmatch(k); m != nil {
			if v != "" {
				i, _ := strconv.Atoi(m[1])
				bs = append(bs, backup{i, string(v)})
			}
			continue
		}
		if k != "id" && k != "record_id" && k != "domain" && v != "" {
			n.SetAttr(attrName(k), dnsx.XMLText(string(v)))
		}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].i < bs[j].i })
	for _, b := range bs {
		n.Children = append(n.Children, withText(el("backup"), b.ip))
	}
	return n
}
