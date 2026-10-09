package kseftest

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Invoice is one invoice the fake holds. Add fills what is left empty.
type Invoice struct {
	Subject               string // Subject1 (issued), Subject2 (received), Subject3
	KsefNumber, Number    string
	SellerNIP, SellerName string
	BuyerNIP, BuyerName   string
	Net, VAT, Gross       float64
	Stored                time.Time // permanentStorageDate
	XML                   []byte
	Hash                  string // SHA-256 of XML, base64 (set by Add)
	BadHash               bool   // metadata carries a wrong invoiceHash
}

// ExportCall is one export request as the fake received it.
type ExportCall struct{ Subject, From, To string }

type job struct {
	key, iv   []byte
	invs      []Invoice
	truncated bool
	last, hwm string
	polls     int
	fail      string
	parts     [][]byte // encrypted
	plain     [][]byte
}

type exports struct {
	invoices []Invoice
	calls    []ExportCall
	jobs     map[string]*job
	stored   map[string][]byte // part path -> encrypted bytes
}

func (e *exports) init() {
	e.jobs = map[string]*job{}
	e.stored = map[string][]byte{}
}

func b64sha(b []byte) string {
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Add stores an invoice, filling KSeF number, parties, amounts and XML.
func (s *Server) Add(inv Invoice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.ex.invoices) + 1
	if inv.Stored.IsZero() {
		inv.Stored = time.Now().Add(-time.Hour)
	}
	if inv.SellerNIP == "" {
		inv.SellerNIP, inv.SellerName, inv.BuyerNIP, inv.BuyerName = "5261040828", "ORLEN S.A.", NIP, "Acme sp. z o.o."
		if inv.Subject == "Subject1" {
			inv.SellerNIP, inv.SellerName, inv.BuyerNIP, inv.BuyerName = NIP, "Acme sp. z o.o.", "1111111111", "F.H.U. Jan Kowalski"
		}
	}
	if inv.KsefNumber == "" {
		inv.KsefNumber = fmt.Sprintf("%s-%s-%012X-%02X", inv.SellerNIP, inv.Stored.UTC().Format("20060102"), 0xA0000000+n, n%256)
	}
	if inv.Number == "" {
		inv.Number = fmt.Sprintf("FA/%d/2026", n)
	}
	if inv.Gross == 0 {
		inv.Net, inv.VAT, inv.Gross = 100, 23, 123
	}
	if inv.XML == nil {
		inv.XML = []byte(fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Faktura xmlns=\"http://crd.gov.pl/wzor/2025/06/25/13775/\">\n"+
			"\t<Podmiot1><NIP>%s</NIP></Podmiot1>\n\t<Fa><P_2>%s</P_2><P_15>%.2f</P_15></Fa>\n</Faktura>\n", inv.SellerNIP, inv.Number, inv.Gross))
	}
	inv.Hash = b64sha(inv.XML)
	s.ex.invoices = append(s.ex.invoices, inv)
}

// Invoices is every invoice the fake holds, in the order added.
func (s *Server) Invoices() []Invoice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Invoice(nil), s.ex.invoices...)
}

// Exports is every export request so far.
func (s *Server) Exports() []ExportCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExportCall(nil), s.ex.calls...)
}

func meta(inv Invoice) map[string]any {
	h := inv.Hash
	if inv.BadHash {
		h = b64sha([]byte("not this"))
	}
	return map[string]any{
		"ksefNumber": inv.KsefNumber, "invoiceNumber": inv.Number,
		"issueDate": inv.Stored.UTC().Format("2006-01-02"), "invoicingDate": stamp(inv.Stored.Add(-time.Second)),
		"acquisitionDate": stamp(inv.Stored.Add(-time.Second)), "permanentStorageDate": stamp(inv.Stored),
		"seller":    map[string]any{"nip": inv.SellerNIP, "name": inv.SellerName},
		"buyer":     map[string]any{"identifier": map[string]any{"type": "Nip", "value": inv.BuyerNIP}, "name": inv.BuyerName},
		"netAmount": inv.Net, "vatAmount": inv.VAT, "grossAmount": inv.Gross, "currency": "PLN",
		"invoicingMode": "Online", "invoiceType": "Vat",
		"formCode":        map[string]any{"systemCode": "FA (3)", "schemaVersion": "1-0E", "value": "FA"},
		"isSelfInvoicing": false, "hasAttachment": false, "invoiceHash": h, "thirdSubjects": []any{},
	}
}

func encrypt(key, iv, p []byte) []byte {
	pad := aes.BlockSize - len(p)%aes.BlockSize
	p = append(append([]byte(nil), p...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	b, _ := aes.NewCipher(key)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(out, p)
	return out
}

// api serves the endpoints that need an access token (s.mu is held).
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case r.Method == "POST" && p == "/invoices/exports":
		s.startExport(w, r)
	case r.Method == "GET" && strings.HasPrefix(p, "/invoices/exports/"):
		s.exportStatus(w, strings.TrimPrefix(p, "/invoices/exports/"))
	case r.Method == "GET" && strings.HasPrefix(p, "/invoices/ksef/"):
		k := strings.TrimPrefix(p, "/invoices/ksef/")
		for _, inv := range s.ex.invoices {
			if inv.KsefNumber == k {
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("X-Ms-Meta-Hash", inv.Hash)
				w.Write(inv.XML)
				return
			}
		}
		writeJSON(w, 404, map[string]any{"exception": map[string]any{"exceptionDetailList": []map[string]any{
			{"exceptionCode": 21164, "exceptionDescription": "Faktura o podanym identyfikatorze nie istnieje."}}}})
	case r.Method == "POST" && p == "/invoices/query/metadata":
		writeJSON(w, 200, map[string]any{"hasMore": false, "isTruncated": false, "invoices": []any{}})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) startExport(w http.ResponseWriter, r *http.Request) {
	if s.Fail429 > 0 {
		s.Fail429--
		w.Header().Set("Retry-After", strconv.Itoa(max(s.RetryAfter, 1)))
		writeJSON(w, 429, map[string]any{"status": map[string]any{"code": 429, "description": "Too Many Requests",
			"details": []string{"Przekroczono limit 20 żądań na godzinę."}}})
		return
	}
	var req struct {
		Encryption struct{ EncryptedSymmetricKey, InitializationVector string }
		Filters    struct {
			SubjectType string
			DateRange   struct {
				DateType, From, To                string
				RestrictToPermanentStorageHwmDate bool
			}
		}
	}
	json.NewDecoder(r.Body).Decode(&req)
	f := req.Filters
	s.ex.calls = append(s.ex.calls, ExportCall{Subject: f.SubjectType, From: f.DateRange.From, To: f.DateRange.To})
	from, err1 := time.Parse(time.RFC3339Nano, f.DateRange.From)
	iv, err2 := base64.StdEncoding.DecodeString(req.Encryption.InitializationVector)
	key := s.Decrypt(req.Encryption.EncryptedSymmetricKey)
	if err1 != nil || err2 != nil || len(key) != 32 || len(iv) != 16 || f.DateRange.DateType != "PermanentStorage" {
		writeJSON(w, 400, map[string]any{"exception": map[string]any{"exceptionDetailList": []map[string]any{
			{"exceptionCode": 21405, "exceptionDescription": "Błąd walidacji danych wejściowych."}}}})
		return
	}
	hwm := time.Now()
	if s.HWM != nil {
		hwm = s.HWM()
	}
	j := &job{key: key, iv: iv, hwm: stamp(hwm)}
	if s.ExportFail != "" && (s.ExportFailFor == "" || s.ExportFailFor == f.SubjectType) {
		j.fail = s.ExportFail
	}
	var sel []Invoice
	for _, inv := range s.ex.invoices {
		if inv.Subject != f.SubjectType || inv.Stored.Before(from) || inv.Stored.After(hwm) {
			continue
		}
		if f.DateRange.To != "" {
			if to, err := time.Parse(time.RFC3339Nano, f.DateRange.To); err == nil && !inv.Stored.Before(to) {
				continue
			}
		}
		sel = append(sel, inv)
	}
	sort.SliceStable(sel, func(i, k int) bool { return sel[i].Stored.Before(sel[k].Stored) })
	if s.TruncateAt > 0 && len(sel) > s.TruncateAt {
		sel, j.truncated = sel[:s.TruncateAt], true
	}
	if len(sel) > 0 {
		j.last = stamp(sel[len(sel)-1].Stored)
	}
	j.invs = sel
	ref := s.next("EX")
	s.ex.jobs[ref] = j
	writeJSON(w, 201, map[string]any{"referenceNumber": ref})
}

func (s *Server) exportStatus(w http.ResponseWriter, ref string) {
	j, ok := s.ex.jobs[ref]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	if j.polls++; j.polls <= s.PendingPolls {
		writeJSON(w, 200, map[string]any{"status": map[string]any{"code": 100, "description": "Eksport faktur w toku"}})
		return
	}
	if j.fail != "" {
		writeJSON(w, 200, map[string]any{"status": map[string]any{"code": 500, "description": j.fail}})
		return
	}
	if j.parts == nil && len(j.invs) > 0 {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		var metas []any
		for _, inv := range j.invs {
			fw, _ := zw.Create(inv.KsefNumber + ".xml")
			fw.Write(inv.XML)
			metas = append(metas, meta(inv))
		}
		fw, _ := zw.Create("_metadata.json")
		json.NewEncoder(fw).Encode(map[string]any{"invoices": metas})
		zw.Close()
		all := buf.Bytes()
		size := s.PartSize
		if size <= 0 {
			size = len(all)
		}
		for i := 0; i < len(all); i += size {
			chunk := all[i:min(i+size, len(all))]
			j.plain = append(j.plain, chunk)
			j.parts = append(j.parts, encrypt(j.key, j.iv, chunk))
		}
	}
	var parts []any
	for i, enc := range j.parts {
		path := fmt.Sprintf("/storage/%s/%d", ref, i+1)
		served := enc
		if s.DamagePart && i == len(j.parts)-1 {
			served = append([]byte(nil), enc...)
			served[len(served)-1] ^= 0xff
		}
		s.ex.stored[path] = served
		parts = append(parts, map[string]any{"ordinalNumber": i + 1, "partName": ref + "-" + strconv.Itoa(i+1) + ".zip.aes",
			"method": "GET", "url": s.URL + path + "?sig=secret", "partSize": len(j.plain[i]), "partHash": b64sha(j.plain[i]),
			"encryptedPartSize": len(enc), "encryptedPartHash": b64sha(enc), "expirationDate": stamp(time.Now().Add(time.Hour))})
	}
	pkg := map[string]any{"invoiceCount": len(j.invs), "size": 0, "parts": parts, "isTruncated": j.truncated,
		"permanentStorageHwmDate": j.hwm, "compressionType": "Zip"}
	if j.last != "" {
		pkg["lastPermanentStorageDate"] = j.last
	}
	writeJSON(w, 200, map[string]any{"status": map[string]any{"code": 200, "description": "Eksport faktur zakończony sukcesem"}, "package": pkg})
}

// part serves an export part from its pre-signed URL (no bearer token).
func (e *exports) part(w http.ResponseWriter, p string) {
	b, ok := e.stored[p]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(b)
}
