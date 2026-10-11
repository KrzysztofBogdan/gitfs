package ksef

import (
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/ksef/ksefpdf"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// roleRoot holds the metadata files; originals live beside it at the same
// <yyyy>/<mm>/<role>/ path (ksef spec §4).
const roleRoot = "_gfs"

// ksefMonth is <yyyy>/<mm> from the date inside a KSeF number
// (<nip>-<YYYYMMDD>-<technical>-<check>).
func ksefMonth(k string) string {
	parts := strings.Split(k, "-")
	if len(parts) < 2 || len(parts[1]) != 8 {
		return "unknown"
	}
	return parts[1][:4] + "/" + parts[1][4:6]
}

func origPath(role, k string) string { return ksefMonth(k) + "/" + role + "/" + k + ".xml" }
func metaPath(role, k string) string { return roleRoot + "/" + origPath(role, k) }

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], dnsx.XMLText(attrs[i+1]))
		}
	}
	return n
}

func text(name, v string, attrs ...string) *xmltree.Node {
	n := el(name, attrs...)
	if v != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: dnsx.XMLText(v)}}
	}
	return n
}

// stamp is a KSeF time in UTC, as short as it is exact.
func stamp(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func money(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// resourceOf is the metadata file of one invoice in one role, with its
// original's bytes as file fa and, when rendered, its PDF as file pdf (ksef
// spec §5, ksef pdf spec §5.1).
func resourceOf(role string, inv invoice, pdf []byte) adapter.Resource {
	m := inv.meta
	id := role + "/" + m.KsefNumber
	root := el("invoice", "id", id, "ksef", m.KsefNumber, "role", role, "version", m.InvoiceHash)
	if m.IsSelfInvoicing {
		root.SetAttr("self-invoicing", "true")
	}
	if m.HasAttachment {
		root.SetAttr("has-attachment", "true")
	}
	add := func(n *xmltree.Node) { root.Children = append(root.Children, n) }
	opt := func(name, v string) {
		if v != "" {
			add(text(name, v))
		}
	}
	opt("number", m.InvoiceNumber)
	opt("type", m.InvoiceType)
	opt("form", m.FormCode.SystemCode)
	opt("issued", m.IssueDate)
	opt("invoicing", stamp(m.InvoicingDate))
	opt("acquired", stamp(m.AcquisitionDate))
	opt("stored", stamp(m.PermanentStorageDate))
	opt("mode", strings.ToLower(m.InvoicingMode))
	add(text("seller", m.Seller.Name, "nip", m.Seller.Nip))
	add(text("buyer", m.Buyer.Name, "type", strings.ToLower(m.Buyer.Identifier.Type), "id", m.Buyer.Identifier.Value))
	for _, ts := range m.ThirdSubjects {
		add(text("third-party", ts.Name, "type", strings.ToLower(ts.Identifier.Type), "id", ts.Identifier.Value, "role", strconv.Itoa(ts.Role)))
	}
	add(el("amounts", "currency", m.Currency, "net", money(m.NetAmount), "vat", money(m.VatAmount), "gross", money(m.GrossAmount)))
	if m.HashOfCorrectedInvoice != "" {
		add(el("corrects", "hash", m.HashOfCorrectedInvoice))
	}
	add(el("file", "id", "fa", "name", m.KsefNumber+".xml", "size", strconv.Itoa(len(inv.xml)), "sha256", m.InvoiceHash))
	files := map[string][]byte{"fa": inv.xml}
	if pdf != nil {
		add(el("file", "id", "pdf", "name", m.KsefNumber+".pdf", "size", strconv.Itoa(len(pdf)), "sha256", b64sha(pdf), "generator", ksefpdf.Release()))
		files["pdf"] = pdf
	}
	return adapter.Resource{ID: id, Version: m.InvoiceHash, Path: metaPath(role, m.KsefNumber), Root: root, Files: files}
}
