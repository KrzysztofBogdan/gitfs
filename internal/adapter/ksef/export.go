package ksef

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// window is the longest export range gfs asks for: KSeF allows 100 days,
// counted in UTC or Polish time; 90 leaves room for either.
const window = 90 * 24 * time.Hour

type party struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// apiInvoice is KSeF's InvoiceMetadata (an export's _metadata.json).
type apiInvoice struct {
	KsefNumber           string `json:"ksefNumber"`
	InvoiceNumber        string `json:"invoiceNumber"`
	IssueDate            string `json:"issueDate"`
	InvoicingDate        string `json:"invoicingDate"`
	AcquisitionDate      string `json:"acquisitionDate"`
	PermanentStorageDate string `json:"permanentStorageDate"`
	Seller               struct {
		Nip  string `json:"nip"`
		Name string `json:"name"`
	} `json:"seller"`
	Buyer struct {
		Identifier party  `json:"identifier"`
		Name       string `json:"name"`
	} `json:"buyer"`
	NetAmount     float64 `json:"netAmount"`
	GrossAmount   float64 `json:"grossAmount"`
	VatAmount     float64 `json:"vatAmount"`
	Currency      string  `json:"currency"`
	InvoicingMode string  `json:"invoicingMode"`
	InvoiceType   string  `json:"invoiceType"`
	FormCode      struct {
		SystemCode    string `json:"systemCode"`
		SchemaVersion string `json:"schemaVersion"`
		Value         string `json:"value"`
	} `json:"formCode"`
	IsSelfInvoicing        bool   `json:"isSelfInvoicing"`
	HasAttachment          bool   `json:"hasAttachment"`
	InvoiceHash            string `json:"invoiceHash"`
	HashOfCorrectedInvoice string `json:"hashOfCorrectedInvoice"`
	ThirdSubjects          []struct {
		Identifier party  `json:"identifier"`
		Name       string `json:"name"`
		Role       int    `json:"role"`
	} `json:"thirdSubjects"`
}

// invoice is one invoice of an export: its metadata and its original bytes.
type invoice struct {
	meta apiInvoice
	xml  []byte
}

type exportPage struct {
	invoices  []invoice
	truncated bool
	hwm, last string // permanentStorageHwmDate, lastPermanentStorageDate
}

type aesKey struct{ key, iv []byte }

func (k aesKey) decrypt(c []byte) ([]byte, error) {
	if len(c) == 0 || len(c)%aes.BlockSize != 0 {
		return nil, errors.New("not whole AES blocks")
	}
	b, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(c))
	cipher.NewCBCDecrypter(b, k.iv).CryptBlocks(out, c)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > aes.BlockSize || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("bad padding")
	}
	return out[:len(out)-pad], nil
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

type exportStatus struct {
	Status struct {
		Code        int      `json:"code"`
		Description string   `json:"description"`
		Details     []string `json:"details"`
	} `json:"status"`
	Package *struct {
		IsTruncated              bool   `json:"isTruncated"`
		LastPermanentStorageDate string `json:"lastPermanentStorageDate"`
		PermanentStorageHwmDate  string `json:"permanentStorageHwmDate"`
		Parts                    []struct {
			OrdinalNumber     int    `json:"ordinalNumber"`
			URL               string `json:"url"`
			Method            string `json:"method"`
			PartHash          string `json:"partHash"`
			EncryptedPartHash string `json:"encryptedPartHash"`
		} `json:"parts"`
	} `json:"package"`
}

// maxLimitWait is the longest Retry-After an export start waits out: KSeF's
// export limit is per hour (ksef spec §6.5).
const maxLimitWait = 65 * time.Minute

// startExport is POST /invoices/exports. A 429 longer than the shared retry
// policy waits (the hourly limit of a large clone) is waited out, at most
// three times, so a clone takes longer instead of failing and starting over.
func (c *Client) startExport(ctx context.Context, req any, out any) error {
	for attempt := 1; ; attempt++ {
		err := c.json(ctx, http.MethodPost, "/invoices/exports", true, req, out)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests || attempt > 3 {
			return err
		}
		n, perr := strconv.Atoi(ae.RetryAfter)
		d := time.Duration(n) * time.Second
		if perr != nil || n <= 0 || d > maxLimitWait {
			return err
		}
		c.OnWait(fmt.Sprintf("KSeF export limit reached, waiting %d min…", (n+59)/60))
		if err := c.Sleep(ctx, d); err != nil {
			return err
		}
	}
}

// export runs one export (ksef spec §6.1) and returns its invoices, each
// checked against KSeF's hash.
func (c *Client) export(ctx context.Context, subject string, from time.Time, to *time.Time) (exportPage, error) {
	where := fmt.Sprintf("export of %s from %s", subject, utc(from))
	fail := func(err error) (exportPage, error) { return exportPage{}, fmt.Errorf("%s: %w", where, err) }
	pub, err := c.key(ctx, "SymmetricKeyEncryption")
	if err != nil {
		return fail(err)
	}
	k := aesKey{make([]byte, 32), make([]byte, 16)}
	rand.Read(k.key)
	rand.Read(k.iv)
	encKey, err := encryptOAEP(pub, k.key)
	if err != nil {
		return fail(err)
	}
	dr := map[string]any{"dateType": "PermanentStorage", "from": utc(from), "restrictToPermanentStorageHwmDate": true}
	if to != nil {
		dr["to"] = utc(*to)
	}
	req := map[string]any{
		"encryption": map[string]string{"encryptedSymmetricKey": encKey, "initializationVector": base64.StdEncoding.EncodeToString(k.iv)},
		"filters":    map[string]any{"subjectType": subject, "dateRange": dr},
	}
	var started struct {
		ReferenceNumber string `json:"referenceNumber"`
	}
	if err := c.startExport(ctx, req, &started); err != nil {
		return fail(err)
	}
	var st exportStatus
	wait := time.Second
	deadline := c.Now().Add(10 * time.Minute)
	for {
		st = exportStatus{}
		if err := c.json(ctx, http.MethodGet, "/invoices/exports/"+started.ReferenceNumber, true, nil, &st); err != nil {
			return fail(err)
		}
		if st.Status.Code == 200 {
			break
		}
		if st.Status.Code >= 400 {
			msg := st.Status.Description
			if len(st.Status.Details) > 0 {
				msg += ": " + strings.Join(st.Status.Details, "; ")
			}
			return fail(errors.New(msg))
		}
		if c.Now().After(deadline) {
			return fail(errors.New("did not finish in 10 minutes"))
		}
		if err := c.Sleep(ctx, wait); err != nil {
			return exportPage{}, err
		}
		wait = min(2*wait, 5*time.Second)
	}
	page := exportPage{}
	if st.Package == nil {
		return page, nil
	}
	page.truncated, page.hwm, page.last = st.Package.IsTruncated, st.Package.PermanentStorageHwmDate, st.Package.LastPermanentStorageDate
	parts := st.Package.Parts
	if len(parts) == 0 {
		return page, nil
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].OrdinalNumber < parts[j].OrdinalNumber })
	var zipped []byte
	for _, p := range parts {
		method := p.Method
		if method == "" {
			method = http.MethodGet
		}
		enc, err := c.raw(ctx, method, p.URL, false)
		if err != nil {
			return fail(err)
		}
		damaged := fmt.Errorf("package part %d is damaged", p.OrdinalNumber)
		if b64sha(enc) != p.EncryptedPartHash {
			return fail(damaged)
		}
		plain, err := k.decrypt(enc)
		if err != nil || b64sha(plain) != p.PartHash {
			return fail(damaged)
		}
		zipped = append(zipped, plain...)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return fail(errors.New("the package is damaged: not a ZIP"))
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return fail(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fail(err)
		}
		files[f.Name] = b
	}
	var meta struct {
		Invoices []apiInvoice `json:"invoices"`
	}
	if err := json.Unmarshal(files["_metadata.json"], &meta); err != nil {
		return fail(fmt.Errorf("_metadata.json: %w", err))
	}
	for _, m := range meta.Invoices {
		x, ok := files[m.KsefNumber+".xml"]
		if !ok {
			return fail(fmt.Errorf("invoice %s is missing from the package", m.KsefNumber))
		}
		if b64sha(x) != m.InvoiceHash {
			return fail(fmt.Errorf("invoice %s: content does not match KSeF's hash", m.KsefNumber))
		}
		page.invoices = append(page.invoices, invoice{meta: m, xml: x})
	}
	return page, nil
}

// exportRole reads every invoice of subject stored since from (ksef spec
// §6.2): 90-day windows, the last one open, a truncated package continued
// from its last storage date. next is where the following pull starts.
func (c *Client) exportRole(ctx context.Context, subject string, from, now time.Time) (invs []invoice, next time.Time, err error) {
	seen := map[string]bool{}
	for {
		// A window is closed only well before now: one ending past the HWM
		// would skip the invoices between the two.
		var to *time.Time
		if end := from.Add(window); end.Before(now.Add(-time.Hour)) {
			to = &end
		}
		page, err := c.export(ctx, subject, from, to)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, inv := range page.invoices {
			if !seen[inv.meta.KsefNumber] {
				seen[inv.meta.KsefNumber] = true
				invs = append(invs, inv)
			}
		}
		switch {
		case page.truncated:
			last, err := time.Parse(time.RFC3339Nano, page.last)
			if err != nil || !last.After(from) {
				return nil, time.Time{}, fmt.Errorf("export of %s from %s: truncated without progress", subject, utc(from))
			}
			from = last
		case to != nil:
			from = *to
		default:
			if hwm, err := time.Parse(time.RFC3339Nano, page.hwm); err == nil && hwm.After(from) {
				from = hwm
			}
			return invs, from, nil
		}
	}
}
