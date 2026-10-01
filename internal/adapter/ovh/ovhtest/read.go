package ovhtest

import (
	"net/http"
	"sort"
	"strconv"
)

func init() {
	registrars = append(registrars, func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("GET /1.0/domain/zone", s.listZones)
		mux.HandleFunc("GET /1.0/domain/zone/{z}", s.zoneRead(func(z *Zone, r *http.Request) any {
			return map[string]any{"name": z.Name, "dnssecSupported": z.DNSSEC != "", "hasDnsAnycast": false,
				"lastUpdate": z.LastUpdate.Format("2006-01-02T15:04:05.000000-07:00"), "nameServers": []string{"dns101.ovh.net", "ns101.ovh.net"}}
		}))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/export", s.zoneRead(func(z *Zone, r *http.Request) any { return z.export() }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/soa", s.zoneRead(func(z *Zone, r *http.Request) any { return z.SOA }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/dnssec", s.zoneRead(func(z *Zone, r *http.Request) any {
			if z.DNSSEC == "" {
				return nil
			}
			return map[string]string{"status": z.DNSSEC}
		}))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/record", s.zoneRead(func(z *Zone, r *http.Request) any {
			ids := []int64{}
			for id, rec := range z.Records {
				q := r.URL.Query()
				if ft := q.Get("fieldType"); ft != "" && rec.FieldType != ft {
					continue
				}
				if q.Has("subDomain") && rec.SubDomain != q.Get("subDomain") {
					continue
				}
				ids = append(ids, id)
			}
			return sortIDs(ids)
		}))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/record/{id}", s.byID(func(z *Zone, id int64) any { return nilIf(z.Records[id]) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/redirection", s.zoneRead(func(z *Zone, r *http.Request) any { return keys64(z.Redirects) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/redirection/{id}", s.byID(func(z *Zone, id int64) any { return nilIf(z.Redirects[id]) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/dynHost/record", s.zoneRead(func(z *Zone, r *http.Request) any { return keys64(z.DynHosts) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/dynHost/record/{id}", s.byID(func(z *Zone, id int64) any { return nilIf(z.DynHosts[id]) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/dynHost/login", s.zoneRead(func(z *Zone, r *http.Request) any { return sortedKeys(z.Logins) }))
		mux.HandleFunc("GET /1.0/domain/zone/{z}/dynHost/login/{login}", s.zoneRead(func(z *Zone, r *http.Request) any {
			return nilIf(z.Logins[r.PathValue("login")])
		}))
	})
}

// nilIf turns a typed nil pointer into an untyped nil (404).
func nilIf[T any](p *T) any {
	if p == nil {
		return nil
	}
	return p
}

func sortIDs(ids []int64) []int64 {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func keys64[V any](m map[int64]V) []int64 {
	ids := []int64{}
	for id := range m {
		ids = append(ids, id)
	}
	return sortIDs(ids)
}

func (s *Server) listZones(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, sortedKeys(s.zones))
}

// zoneRead serves f's answer for zone {z}; nil is 404.
func (s *Server) zoneRead(f func(*Zone, *http.Request) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		z := s.zones[r.PathValue("z")]
		if z == nil {
			writeError(w, http.StatusNotFound, "This service does not exist")
			return
		}
		v := f(z, r)
		if v == nil {
			writeError(w, http.StatusNotFound, "The requested object does not exist")
			return
		}
		writeJSON(w, v)
	}
}

func (s *Server) byID(f func(*Zone, int64) any) http.HandlerFunc {
	return s.zoneRead(func(z *Zone, r *http.Request) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return nil
		}
		return f(z, id)
	})
}
