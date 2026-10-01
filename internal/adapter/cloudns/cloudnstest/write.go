package cloudnstest

import (
	"slices"
	"strconv"
)

// recordFields are add-record/mod-record parameters stored as the record's
// own fields; any other non-auth parameter is a type-specific Extra.
var recordFields = map[string]bool{"auth-id": true, "sub-auth-id": true, "auth-password": true, "domain-name": true,
	"record-id": true, "record-type": true, "host": true, "record": true, "ttl": true, "status": true,
	"geodns-location": true, "geodns-code": true}

func (s *Server) recordFrom(z *Zone, f form, r *Record) map[string]string {
	ttl := f.get("ttl")
	if n, err := strconv.Atoi(ttl); err != nil || !slices.Contains(TTLs, n) {
		return failed("Invalid TTL")
	}
	r.Host, r.Record, r.TTL = f.get("host"), f.get("record"), ttl
	if st := f.get("status"); st == "0" {
		r.Status = 0
	} else if st == "1" {
		r.Status = 1
	}
	if g := f.get("geodns-location"); g != "" {
		if z.Kind != "geodns" {
			return failed("GeoDNS is not available for this zone")
		}
		r.Geo = g
	}
	r.Extra = map[string]string{}
	for k := range f.r.PostForm {
		if !recordFields[k] {
			r.Extra[k] = f.get(k)
		}
	}
	return nil
}

func init() {
	registrars = append(registrars, func(s *Server) {
		s.handle("add-record", func(s *Server, f form) any {
			z := s.zone(f)
			t := f.get("record-type")
			if !slices.Contains(Types, t) {
				return failed("Invalid record type")
			}
			r := &Record{Type: t, Status: 1}
			if e := s.recordFrom(z, f, r); e != nil {
				return e
			}
			if z.Kind == "geodns" && geoTypes[t] && r.Geo == "" {
				r.Geo = "1"
			}
			r.ID = s.nextID()
			z.Records[r.ID] = r
			z.Serial++
			n, _ := strconv.Atoi(r.ID)
			return map[string]any{"status": "Success", "statusDescription": "The record was added successfully.", "data": map[string]any{"id": n}}
		})
		s.handle("mod-record", func(s *Server, f form) any {
			z := s.zone(f)
			r := z.Records[f.get("record-id")]
			if r == nil {
				return failed("Missing or invalid record-id.")
			}
			cp := *r
			if e := s.recordFrom(z, f, &cp); e != nil {
				return e
			}
			*r = cp
			z.Serial++
			return success("The record was modified successfully.")
		})
		s.handle("delete-record", func(s *Server, f form) any {
			z := s.zone(f)
			if z.Records[f.get("record-id")] == nil {
				return failed("Missing or invalid record-id.")
			}
			delete(z.Records, f.get("record-id"))
			delete(z.Failover, f.get("record-id"))
			z.Serial++
			return success("The record was deleted successfully.")
		})
		s.handle("modify-soa", func(s *Server, f form) any {
			z := s.zone(f)
			z.SOA = SOA{Primary: f.get("primary-ns"), Admin: f.get("admin-mail"), Refresh: f.get("refresh"), Retry: f.get("retry"),
				Expire: f.get("expire"), TTL: f.get("default-ttl")}
			z.Serial++
			return success("The SOA record was modified successfully.")
		})
		s.handle("activate-dnssec", func(s *Server, f form) any { s.zone(f).DNSSEC = true; return success("DNSSEC was activated.") })
		s.handle("deactivate-dnssec", func(s *Server, f form) any { s.zone(f).DNSSEC = false; return success("DNSSEC was deactivated.") })
		s.handle("change-status", func(s *Server, f form) any {
			s.zone(f).Active = f.get("status") == "1"
			return success("The zone status was changed.")
		})
		s.handle("add-mail-forward", func(s *Server, f form) any {
			z := s.zone(f)
			m := &MailForward{ID: s.nextID(), Box: f.get("box"), Host: f.get("host"), Destination: f.get("destination")}
			z.MailForwards[m.ID] = m
			return success("The mail forward was added successfully.")
		})
		s.handle("modify-mail-forward", func(s *Server, f form) any {
			m := s.zone(f).MailForwards[f.get("mail-forward-id")]
			if m == nil {
				return failed("Invalid mail forward.")
			}
			m.Box, m.Host, m.Destination = f.get("box"), f.get("host"), f.get("destination")
			return success("The mail forward was modified successfully.")
		})
		s.handle("delete-mail-forward", func(s *Server, f form) any {
			z := s.zone(f)
			if z.MailForwards[f.get("mail-forward-id")] == nil {
				return failed("Invalid mail forward.")
			}
			delete(z.MailForwards, f.get("mail-forward-id"))
			return success("The mail forward was deleted successfully.")
		})
		failoverSet := func(s *Server, f form, create bool) any {
			z := s.zone(f)
			r := z.Records[f.get("record-id")]
			if r == nil {
				return failed("Missing or invalid record-id.")
			}
			if create == r.Failover {
				if create {
					return failed("Failover is already activated for this record.")
				}
				return failed("Failover is not activated for this record.")
			}
			st := map[string]string{}
			for k := range f.r.PostForm {
				if !recordFields[k] {
					st[k] = f.get(k)
				}
			}
			if st["check_type"] == "" {
				return failed("Missing check_type.")
			}
			z.Failover[r.ID] = st
			r.Failover = true
			return success("Failover settings were saved.")
		}
		s.handle("failover-activate", func(s *Server, f form) any { return failoverSet(s, f, true) })
		s.handle("failover-modify", func(s *Server, f form) any { return failoverSet(s, f, false) })
		s.handle("failover-deactivate", func(s *Server, f form) any {
			z := s.zone(f)
			r := z.Records[f.get("record-id")]
			if r == nil || !r.Failover {
				return failed("Failover is not activated for this record.")
			}
			delete(z.Failover, r.ID)
			r.Failover = false
			return success("Failover was deactivated.")
		})
	})
}

// Writes lists the write actions called so far, in order.
func (s *Server) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.Requests {
		switch {
		case a == "login", a == "records", a == "soa-details", a == "mail-forwards", a == "failover-settings",
			len(a) > 4 && (a[:4] == "get-" || a[:3] == "is-" || a[:5] == "list-"):
			continue
		}
		out = append(out, a)
	}
	return out
}
