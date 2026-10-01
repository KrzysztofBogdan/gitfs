package ovhtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Record is an OVH zone record (/domain/zone/{z}/record/{id}).
type Record struct {
	ID        int64  `json:"id"`
	FieldType string `json:"fieldType"`
	SubDomain string `json:"subDomain"`
	Target    string `json:"target"`
	TTL       int64  `json:"ttl"`
	Zone      string `json:"zone"`
}

// Redirect is a web redirection; OVH backs it with an A record of the same
// id and a TXT record "<n>|<target>" at the same name.
type Redirect struct {
	ID          int64   `json:"id"`
	SubDomain   string  `json:"subDomain"`
	Target      string  `json:"target"`
	Type        string  `json:"type"`
	Title       *string `json:"title"`
	Keywords    *string `json:"keywords"`
	Description *string `json:"description"`
	Zone        string  `json:"zone"`
	txt         int64   // id of the owned TXT record
}

// DynHost is a DynHost record; OVH backs it with the A record of the same id.
type DynHost struct {
	ID        int64  `json:"id"`
	SubDomain string `json:"subDomain"`
	IP        string `json:"ip"`
	TTL       int64  `json:"ttl"`
	Type      string `json:"type"`
	Zone      string `json:"zone"`
}

// Login is a DynHost login.
type Login struct {
	Login     string `json:"login"`
	SubDomain string `json:"subDomain"`
	Zone      string `json:"zone"`
	Password  string `json:"-"`
}

type SOA struct {
	Server      string `json:"server"`
	Email       string `json:"email"`
	Serial      int64  `json:"serial"`
	Refresh     int64  `json:"refresh"`
	Expire      int64  `json:"expire"`
	NxDomainTTL int64  `json:"nxDomainTtl"`
	TTL         int64  `json:"ttl"`
}

type Zone struct {
	Name       string
	DNSSEC     string // "enabled", "disabled", "enableInProgress", "disableInProgress"; "" = not supported
	SOA        SOA
	Records    map[int64]*Record
	Redirects  map[int64]*Redirect
	DynHosts   map[int64]*DynHost
	Logins     map[string]*Login
	Refreshes  int  // POST /refresh calls
	Dirty      bool // changed since the last refresh
	LastUpdate time.Time
}

// AddZone creates a zone with OVH's default SOA and NS records.
func (s *Server) AddZone(name string) *Zone {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := &Zone{Name: name, DNSSEC: "disabled",
		SOA:     SOA{Server: "dns101.ovh.net.", Email: "tech.ovh.net.", Serial: 2025101600, Refresh: 86400, Expire: 3600000, NxDomainTTL: 60, TTL: 3600},
		Records: map[int64]*Record{}, Redirects: map[int64]*Redirect{}, DynHosts: map[int64]*DynHost{}, Logins: map[string]*Login{},
		LastUpdate: time.Date(2025, 1, 13, 9, 57, 55, 0, time.UTC)}
	s.zones[name] = z
	for _, ns := range []string{"dns101.ovh.net.", "ns101.ovh.net."} {
		s.addRecord(z, Record{FieldType: "NS", Target: ns})
	}
	return z
}

func (s *Server) addRecord(z *Zone, r Record) *Record {
	if r.ID == 0 {
		r.ID = s.nextID()
	}
	r.Zone = z.Name
	z.Records[r.ID] = &r
	z.Dirty = true
	return &r
}

// AddRecord adds a record to zone and returns it with its id.
func (s *Server) AddRecord(zone string, r Record) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addRecord(s.zones[zone], r)
}

var redirectCode = map[string]string{"visible": "1", "visiblePermanent": "2", "invisible": "3"}

func (s *Server) addRedirect(z *Zone, rd Redirect) *Redirect {
	rd.ID = s.nextID()
	rd.Zone = z.Name
	s.addRecord(z, Record{ID: rd.ID, FieldType: "A", SubDomain: rd.SubDomain, Target: "213.186.33.5"})
	rd.txt = s.addRecord(z, Record{FieldType: "TXT", SubDomain: rd.SubDomain, TTL: 600, Target: redirectCode[rd.Type] + "|" + rd.Target}).ID
	z.Redirects[rd.ID] = &rd
	return &rd
}

// AddRedirect adds a redirection with its owned A and TXT records.
func (s *Server) AddRedirect(zone string, rd Redirect) *Redirect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addRedirect(s.zones[zone], rd)
}

func (s *Server) addDynHost(z *Zone, d DynHost) *DynHost {
	d.ID = s.nextID()
	d.Zone, d.Type = z.Name, "A"
	if d.TTL == 0 {
		d.TTL = 60
	}
	s.addRecord(z, Record{ID: d.ID, FieldType: "A", SubDomain: d.SubDomain, Target: d.IP, TTL: d.TTL})
	z.DynHosts[d.ID] = &d
	return &d
}

// AddDynHost adds a DynHost record with its owned A record.
func (s *Server) AddDynHost(zone string, d DynHost) *DynHost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDynHost(s.zones[zone], d)
}

// AddLogin adds a DynHost login.
func (s *Server) AddLogin(zone string, l Login) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.Zone = zone
	s.zones[zone].Logins[l.Login] = &l
}

// Zone returns the zone for inspection (callers must not race requests).
func (s *Server) Zone(name string) *Zone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.zones[name]
}

// export is the zone as BIND text, like GET /export.
func (z *Zone) export() string {
	var b strings.Builder
	fmt.Fprintf(&b, "$TTL %d\n@\tIN SOA %s %s (%d %d 3600 %d %d)\n", z.SOA.TTL, z.SOA.Server, z.SOA.Email, z.SOA.Serial, z.SOA.Refresh, z.SOA.Expire, z.SOA.NxDomainTTL)
	rs := make([]*Record, 0, len(z.Records))
	for _, r := range z.Records {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	for _, r := range rs {
		ttl := ""
		if r.TTL != 0 {
			ttl = fmt.Sprint(r.TTL)
		}
		target := r.Target
		if r.FieldType == "TXT" {
			target = `"` + target + `"`
		}
		fmt.Fprintf(&b, "%s %s IN %s %s\n", r.SubDomain, ttl, r.FieldType, target)
	}
	return b.String()
}
