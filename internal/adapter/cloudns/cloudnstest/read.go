package cloudnstest

import (
	"sort"
	"strconv"
)

// Locations is the GeoDNS location tree get-geodns-locations returns.
var Locations = []map[string]any{
	{"id": "1", "name": "Default", "parent_id": nil, "code": "DEFAULT"},
	{"id": "5", "name": "Europe", "parent_id": "1", "code": "EUR"},
	{"id": "6", "name": "North America", "parent_id": "1", "code": "NAM"},
	{"id": "241", "name": "United States", "parent_id": "6", "code": "US"},
}

// TTLs and Types are what the account allows (as on qa1.pl).
var (
	TTLs  = []int{60, 300, 600, 900, 1800, 3600, 21600, 43200, 86400, 172800, 259200, 604800, 1209600, 2592000}
	Types = []string{"A", "AAAA", "MX", "CNAME", "TXT", "SPF", "NS", "WR", "SRV", "ALIAS", "RP", "SSHFP", "NAPTR", "CAA",
		"TLSA", "DS", "CERT", "PTR", "HINFO", "LOC", "DNAME", "SMIMEA", "SVCB", "HTTPS", "OPENPGPKEY"}
)

func page(f form) (int, int) {
	p, _ := strconv.Atoi(f.get("page"))
	n, _ := strconv.Atoi(f.get("rows-per-page"))
	return max(p, 1), max(n, 10)
}

func window[T any](xs []T, p, n int) []T {
	lo := (p - 1) * n
	if lo >= len(xs) {
		return nil
	}
	return xs[lo:min(lo+n, len(xs))]
}

func (z *Zone) zoneJSON() map[string]any {
	status := "0"
	if z.Active {
		status = "1"
	}
	return map[string]any{"name": z.Name, "id": "7466234", "type": z.Type, "hasBulk": false, "is_cloud": 0, "zone": z.Kind,
		"status": status, "serial": itoa(z.Serial), "isUpdated": 1}
}

func (s *Server) zone(f form) *Zone { return s.zones[f.get("domain-name")] }

func (r *Record) json(z *Zone) map[string]any {
	m := map[string]any{"id": r.ID, "type": r.Type, "host": r.Host, "record": r.Record, "ttl": r.TTL, "status": r.Status,
		"failover": "0"}
	if r.Failover {
		m["failover"] = "1"
	}
	if r.Type == "A" || r.Type == "AAAA" {
		m["dynamicurl_status"] = 0
	}
	if r.Geo != "" {
		for _, l := range Locations {
			if l["id"] == r.Geo {
				m["geodns-location"] = r.Geo
				if r.Type == "A" { // the real API mixes strings and numbers here
					n, _ := strconv.Atoi(r.Geo)
					m["geodns-location"] = n
				}
				m["geodns-location-name"], m["geodns-location-code"] = l["name"], l["code"]
			}
		}
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return m
}

func init() {
	registrars = append(registrars, func(s *Server) {
		s.handle("list-zones", func(s *Server, f form) any {
			p, n := page(f)
			var out []any
			for _, name := range window(sortedKeys(s.zones), p, n) {
				out = append(out, s.zones[name].zoneJSON())
			}
			if out == nil {
				return []any{}
			}
			return out
		})
		s.handle("get-pages-count", func(s *Server, f form) any {
			_, n := page(f)
			return (len(s.zones) + n - 1) / n
		})
		s.handle("get-zone-info", func(s *Server, f form) any {
			z := s.zone(f)
			if z == nil {
				return failed("Missing domain-name")
			}
			j := z.zoneJSON()
			return map[string]any{"name": j["name"], "type": j["type"], "zone": j["zone"], "status": j["status"]}
		})
		s.handle("records", func(s *Server, f form) any {
			z := s.zone(f)
			if z == nil {
				return failed("Missing domain-name")
			}
			ids := sortedKeys(z.Records)
			sort.Slice(ids, func(i, j int) bool { a, _ := strconv.Atoi(ids[i]); b, _ := strconv.Atoi(ids[j]); return a < b })
			p, n := page(f)
			out := map[string]any{}
			for _, id := range window(ids, p, n) {
				out[id] = z.Records[id].json(z)
			}
			if len(out) == 0 {
				return []any{}
			}
			return out
		})
		s.handle("get-records-count", func(s *Server, f form) any { return itoa(len(s.zone(f).Records)) })
		s.handle("soa-details", func(s *Server, f form) any {
			z := s.zone(f)
			return map[string]string{"serialNumber": itoa(z.Serial), "primaryNS": z.SOA.Primary, "adminMail": z.SOA.Admin,
				"refresh": z.SOA.Refresh, "retry": z.SOA.Retry, "expire": z.SOA.Expire, "defaultTTL": z.SOA.TTL}
		})
		s.handle("is-dnssec-available", func(s *Server, f form) any {
			if s.zone(f).DNSSECAvailable {
				return 1
			}
			return 0
		})
		s.handle("get-dnssec-ds-records", func(s *Server, f form) any {
			z := s.zone(f)
			if !z.DNSSEC {
				return map[string]any{"status": "0"}
			}
			return map[string]any{"status": "1", "ds": []string{z.Name + ". 3600 IN DS 12626 13 2 B156B918CC62"},
				"ds_records": []map[string]string{{"digest": "B156B918CC62", "key_tag": "12626", "algorithm": "13",
					"algorithm_name": "ECDSA SHA-256", "digest_type": "2", "digest_type_name": "SHA-256"}}}
		})
		s.handle("mail-forwards", func(s *Server, f form) any {
			out := map[string]any{}
			for id, m := range s.zone(f).MailForwards {
				out[id] = map[string]string{"id": m.ID, "box": m.Box, "host": m.Host, "destination": m.Destination, "status": "1"}
			}
			return out // {} when empty, unlike records
		})
		s.handle("failover-settings", func(s *Server, f form) any {
			z := s.zone(f)
			st := z.Failover[f.get("record-id")]
			if st == nil {
				return failed("Failover is not activated for this record.")
			}
			return st
		})
		s.handle("get-geodns-locations", func(s *Server, f form) any {
			if z := s.zone(f); z == nil || z.Kind != "geodns" {
				return failed("Missing domain-name") // as ClouDNS: locations are asked for per GeoDNS zone
			}
			return Locations
		})
		s.handle("get-available-ttl", func(*Server, form) any { return TTLs })
		s.handle("get-available-record-types", func(*Server, form) any { return Types })
	})
}
