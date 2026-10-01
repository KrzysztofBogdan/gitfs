package cloudnstest

import (
	"sort"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Record holds what records.json returns for one record; Extra are the
// type-specific fields (caa_flag, weight, …) as the API names them.
type Record struct {
	ID, Type, Host, Record, TTL string
	Status                      int    // 1 active, 0 inactive
	Geo                         string // geodns-location id; "" outside GeoDNS zones
	Failover                    bool
	Extra                       map[string]string
}

type MailForward struct{ ID, Box, Host, Destination string }

type SOA struct{ Serial, Primary, Admin, Refresh, Retry, Expire, TTL string }

type Zone struct {
	Name, Type, Kind string // Type master/parked/slave; Kind domain/geodns
	Active           bool
	SOA              SOA
	DNSSEC           bool
	DNSSECAvailable  bool
	Records          map[string]*Record
	MailForwards     map[string]*MailForward
	Failover         map[string]map[string]string // record id -> settings as failover-settings returns them
	Serial           int
}

// AddZone creates an active master zone with ClouDNS's GeoDNS name servers.
func (s *Server) AddZone(name, kind string) *Zone {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := &Zone{Name: name, Type: "master", Kind: kind, Active: true, DNSSECAvailable: true, Serial: 2026092801,
		SOA:     SOA{Primary: "gns1.cloudns.net", Admin: "support@cloudns.net", Refresh: "7200", Retry: "1800", Expire: "1209600", TTL: "3600"},
		Records: map[string]*Record{}, MailForwards: map[string]*MailForward{}, Failover: map[string]map[string]string{}}
	s.zones[name] = z
	for _, ns := range []string{"gns1.cloudns.net", "gns2.cloudns.net"} {
		s.addRecord(z, Record{Type: "NS", Record: ns, TTL: "3600"})
	}
	return z
}

func (s *Server) addRecord(z *Zone, r Record) *Record {
	if r.ID == "" {
		r.ID = s.nextID()
	}
	if r.TTL == "" {
		r.TTL = "3600"
	}
	if r.Status == 0 {
		r.Status = 1
	}
	if r.Status == -1 {
		r.Status = 0
	}
	if z.Kind == "geodns" && geoTypes[r.Type] && r.Geo == "" {
		r.Geo = "1"
	}
	z.Records[r.ID] = &r
	z.Serial++
	return &r
}

// AddRecord adds a record (Status -1 adds it inactive).
func (s *Server) AddRecord(zone string, r Record) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addRecord(s.zones[zone], r)
}

// AddMailForward adds a mail forward.
func (s *Server) AddMailForward(zone string, m MailForward) *MailForward {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.ID = s.nextID()
	s.zones[zone].MailForwards[m.ID] = &m
	return &m
}

// SetFailover turns failover on for a record with the given settings.
func (s *Server) SetFailover(zone, recordID string, settings map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z := s.zones[zone]
	z.Failover[recordID] = settings
	z.Records[recordID].Failover = true
}

// Zone returns the zone for inspection (callers must not race requests).
func (s *Server) Zone(name string) *Zone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.zones[name]
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// geoTypes may carry a GeoDNS location.
var geoTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "NAPTR": true, "SRV": true, "ALIAS": true}
