package ovhtest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

var recordTypes = map[string]bool{"A": true, "AAAA": true, "CAA": true, "CNAME": true, "DKIM": true, "DMARC": true, "DNAME": true,
	"HTTPS": true, "LOC": true, "MX": true, "NAPTR": true, "NS": true, "PTR": true, "RP": true, "SPF": true, "SRV": true,
	"SSHFP": true, "SVCB": true, "TLSA": true, "TXT": true}

func init() {
	registrars = append(registrars, func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /1.0/domain/zone/{z}/record", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			ft, _ := body["fieldType"].(string)
			if !recordTypes[ft] {
				return nil, 400, "Invalid field type"
			}
			rec := s.addRecord(z, Record{FieldType: ft, SubDomain: str(body["subDomain"]), Target: str(body["target"]), TTL: num(body["ttl"])})
			return rec, 0, ""
		}))
		mux.HandleFunc("PUT /1.0/domain/zone/{z}/record/{id}", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			rec := z.Records[pathID(r)]
			if rec == nil {
				return nil, 404, "The requested object does not exist"
			}
			if v, ok := body["subDomain"]; ok {
				rec.SubDomain = str(v)
			}
			rec.Target = str(body["target"])
			if v, ok := body["ttl"]; ok {
				rec.TTL = num(v)
			}
			return nil, 0, ""
		}))
		mux.HandleFunc("DELETE /1.0/domain/zone/{z}/record/{id}", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			id := pathID(r)
			if z.Records[id] == nil {
				return nil, 404, "The requested object does not exist"
			}
			delete(z.Records, id)
			return nil, 0, ""
		}))
		mux.HandleFunc("POST /1.0/domain/zone/{z}/redirection", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			t := str(body["type"])
			if redirectCode[t] == "" {
				return nil, 400, "Invalid redirection type"
			}
			rd := s.addRedirect(z, Redirect{SubDomain: str(body["subDomain"]), Target: str(body["target"]), Type: t,
				Title: optStr(body["title"]), Keywords: optStr(body["keywords"]), Description: optStr(body["description"])})
			return rd, 0, ""
		}))
		mux.HandleFunc("PUT /1.0/domain/zone/{z}/redirection/{id}", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			rd := z.Redirects[pathID(r)]
			if rd == nil {
				return nil, 404, "The requested object does not exist"
			}
			rd.Target, rd.Type = str(body["target"]), str(body["type"])
			rd.Title, rd.Keywords, rd.Description = optStr(body["title"]), optStr(body["keywords"]), optStr(body["description"])
			if txt := z.Records[rd.txt]; txt != nil {
				txt.Target = redirectCode[rd.Type] + "|" + rd.Target
			}
			return nil, 0, ""
		}))
		mux.HandleFunc("DELETE /1.0/domain/zone/{z}/redirection/{id}", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			rd := z.Redirects[pathID(r)]
			if rd == nil {
				return nil, 404, "The requested object does not exist"
			}
			delete(z.Records, rd.ID)
			delete(z.Records, rd.txt)
			delete(z.Redirects, rd.ID)
			return nil, 0, ""
		}))
		mux.HandleFunc("POST /1.0/domain/zone/{z}/dynHost/record", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			return s.addDynHost(z, DynHost{SubDomain: str(body["subDomain"]), IP: str(body["ip"])}), 0, ""
		}))
		mux.HandleFunc("PUT /1.0/domain/zone/{z}/dynHost/record/{id}", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			d := z.DynHosts[pathID(r)]
			if d == nil {
				return nil, 404, "The requested object does not exist"
			}
			d.SubDomain, d.IP = str(body["subDomain"]), str(body["ip"])
			if a := z.Records[d.ID]; a != nil {
				a.SubDomain, a.Target = d.SubDomain, d.IP
			}
			return nil, 0, ""
		}))
		mux.HandleFunc("DELETE /1.0/domain/zone/{z}/dynHost/record/{id}", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			id := pathID(r)
			if z.DynHosts[id] == nil {
				return nil, 404, "The requested object does not exist"
			}
			delete(z.DynHosts, id)
			delete(z.Records, id)
			return nil, 0, ""
		}))
		mux.HandleFunc("POST /1.0/domain/zone/{z}/dynHost/login", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			pw := str(body["password"])
			if len(pw) < 8 {
				return nil, 400, "Password must be at least 8 characters"
			}
			l := &Login{Login: z.Name + "-" + str(body["loginSuffix"]), SubDomain: str(body["subDomain"]), Zone: z.Name, Password: pw}
			z.Logins[l.Login] = l
			return l, 0, ""
		}))
		mux.HandleFunc("PUT /1.0/domain/zone/{z}/dynHost/login/{login}", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			l := z.Logins[r.PathValue("login")]
			if l == nil {
				return nil, 404, "The requested object does not exist"
			}
			l.SubDomain = str(body["subDomain"])
			return nil, 0, ""
		}))
		mux.HandleFunc("DELETE /1.0/domain/zone/{z}/dynHost/login/{login}", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			if z.Logins[r.PathValue("login")] == nil {
				return nil, 404, "The requested object does not exist"
			}
			delete(z.Logins, r.PathValue("login"))
			return nil, 0, ""
		}))
		mux.HandleFunc("PUT /1.0/domain/zone/{z}/soa", s.zoneWrite(func(z *Zone, r *http.Request, body map[string]any) (any, int, string) {
			z.SOA.TTL, z.SOA.Refresh, z.SOA.Expire, z.SOA.NxDomainTTL = num(body["ttl"]), num(body["refresh"]), num(body["expire"]), num(body["nxDomainTtl"])
			z.SOA.Email = str(body["email"])
			return nil, 0, ""
		}))
		mux.HandleFunc("POST /1.0/domain/zone/{z}/dnssec", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			if z.DNSSEC == "" {
				return nil, 403, "DNSSEC is not supported for this zone"
			}
			z.DNSSEC = "enableInProgress"
			return nil, 0, ""
		}))
		mux.HandleFunc("DELETE /1.0/domain/zone/{z}/dnssec", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			z.DNSSEC = "disableInProgress"
			return nil, 0, ""
		}))
		mux.HandleFunc("POST /1.0/domain/zone/{z}/refresh", s.zoneWrite(func(z *Zone, r *http.Request, _ map[string]any) (any, int, string) {
			z.Refreshes++
			z.SOA.Serial++
			z.Dirty = false
			return nil, 0, ""
		}))
	})
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func optStr(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// zoneWrite decodes the JSON body and runs f on zone {z}; a non-zero status
// is an error answer, else f's value (or null) is written.
func (s *Server) zoneWrite(f func(*Zone, *http.Request, map[string]any) (any, int, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		z := s.zones[r.PathValue("z")]
		if z == nil {
			writeError(w, http.StatusNotFound, "This service does not exist")
			return
		}
		body := map[string]any{}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !strings.Contains(err.Error(), "EOF") {
				writeError(w, http.StatusBadRequest, "Invalid JSON")
				return
			}
		}
		v, status, msg := f(z, r, body)
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/refresh") {
			z.Dirty = true
		}
		writeJSON(w, v)
	}
}

// Calls lists the write requests made so far ("POST /1.0/…"), in order.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.Requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}
