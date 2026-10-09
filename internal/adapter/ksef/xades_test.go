package ksef

// Test helpers for the TEST environment only (ksef spec §10.3): a
// self-signed personal certificate, an enveloped XAdES-BES signature built
// directly in exclusive-c14n form, an online session to send invoices.
// Never used by the adapter.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// randomNIP is a NIP with a valid check digit.
func randomNIP() string {
	for {
		n, _ := rand.Int(rand.Reader, big.NewInt(9e8))
		s := fmt.Sprintf("%09d", n.Int64()+1e8)
		sum := 0
		for i, w := range []int{6, 5, 7, 2, 3, 4, 5, 6, 7} {
			sum += int(s[i]-'0') * w
		}
		if c := sum % 11; c != 10 {
			return s + fmt.Sprint(c)
		}
	}
}

func selfSigned(nip string) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	name := pkix.Name{CommonName: "gfs Tester", Country: []string{"PL"}, SerialNumber: "TINPL-" + nip,
		ExtraNames: []pkix.AttributeTypeAndValue{
			{Type: asn1.ObjectIdentifier{2, 5, 4, 42}, Value: "gfs"},
			{Type: asn1.ObjectIdentifier{2, 5, 4, 4}, Value: "Tester"},
		}}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: name, Issuer: name,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

const (
	dsNS    = "http://www.w3.org/2000/09/xmldsig#"
	xadesNS = "http://uri.etsi.org/01903/v1.3.2#"
	excC14N = "http://www.w3.org/2001/10/xml-exc-c14n#"
	sha256U = "http://www.w3.org/2001/04/xmlenc#sha256"
)

// signXAdES signs doc, which must already be in canonical form (no
// declaration, no whitespace, only the default namespace).
func signXAdES(doc string, cert *x509.Certificate, key *rsa.PrivateKey) (string, error) {
	signingTime := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05Z")
	ds := func(name, inner string) string {
		return `<ds:` + name + ` xmlns:ds="` + dsNS + `">` + inner + `</ds:` + name + `>`
	}
	signedProps := `<xades:SignedProperties xmlns:xades="` + xadesNS + `" Id="SignedProperties">` +
		`<xades:SignedSignatureProperties><xades:SigningTime>` + signingTime + `</xades:SigningTime>` +
		`<xades:SigningCertificate><xades:Cert><xades:CertDigest>` +
		`<ds:DigestMethod xmlns:ds="` + dsNS + `" Algorithm="` + sha256U + `"></ds:DigestMethod>` +
		ds("DigestValue", b64sha(cert.Raw)) + `</xades:CertDigest>` +
		`<xades:IssuerSerial>` + ds("X509IssuerName", cert.Issuer.String()) + ds("X509SerialNumber", cert.SerialNumber.String()) +
		`</xades:IssuerSerial></xades:Cert></xades:SigningCertificate></xades:SignedSignatureProperties></xades:SignedProperties>`
	ref := func(uri, typ, digest string, enveloped bool) string {
		s := `<ds:Reference`
		if typ != "" {
			s += ` Type="` + typ + `"`
		}
		s += ` URI="` + uri + `"><ds:Transforms>`
		if enveloped {
			s += `<ds:Transform Algorithm="http://www.w3.org/2000/09/xmldsig#enveloped-signature"></ds:Transform>`
		}
		return s + `<ds:Transform Algorithm="` + excC14N + `"></ds:Transform></ds:Transforms>` +
			`<ds:DigestMethod Algorithm="` + sha256U + `"></ds:DigestMethod><ds:DigestValue>` + digest + `</ds:DigestValue></ds:Reference>`
	}
	signedInfo := `<ds:SignedInfo xmlns:ds="` + dsNS + `">` +
		`<ds:CanonicalizationMethod Algorithm="` + excC14N + `"></ds:CanonicalizationMethod>` +
		`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"></ds:SignatureMethod>` +
		ref("", "", b64sha([]byte(doc)), true) +
		ref("#SignedProperties", "http://uri.etsi.org/01903#SignedProperties", b64sha([]byte(signedProps)), false) +
		`</ds:SignedInfo>`
	h := sha256.Sum256([]byte(signedInfo))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	signature := `<ds:Signature xmlns:ds="` + dsNS + `" Id="Signature">` + signedInfo +
		`<ds:SignatureValue>` + base64.StdEncoding.EncodeToString(sig) + `</ds:SignatureValue>` +
		`<ds:KeyInfo><ds:X509Data><ds:X509Certificate>` + base64.StdEncoding.EncodeToString(cert.Raw) +
		`</ds:X509Certificate></ds:X509Data></ds:KeyInfo>` +
		`<ds:Object><xades:QualifyingProperties xmlns:xades="` + xadesNS + `" Target="#Signature">` + signedProps +
		`</xades:QualifyingProperties></ds:Object></ds:Signature>`
	i := strings.LastIndex(doc, "</")
	return `<?xml version="1.0" encoding="utf-8"?>` + doc[:i] + signature + doc[i:], nil
}

// loginXAdES gives c an access token by a self-signed XAdES signature.
func loginXAdES(ctx context.Context, c *Client) error {
	cert, key, err := selfSigned(c.NIP)
	if err != nil {
		return err
	}
	var ch struct{ Challenge string }
	if err := c.json(ctx, http.MethodPost, "/auth/challenge", false, nil, &ch); err != nil {
		return err
	}
	doc := `<AuthTokenRequest xmlns="http://ksef.mf.gov.pl/auth/token/2.0"><Challenge>` + ch.Challenge +
		`</Challenge><ContextIdentifier><Nip>` + c.NIP + `</Nip></ContextIdentifier>` +
		`<SubjectIdentifierType>certificateSubject</SubjectIdentifierType></AuthTokenRequest>`
	signed, err := signXAdES(doc, cert, key)
	if err != nil {
		return err
	}
	resp, err := c.hc.Post(c.Base+"/auth/xades-signature?verifyCertificateChain=false", "application/xml", strings.NewReader(signed))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("xades-signature: %d %s", resp.StatusCode, apiMessage(resp.StatusCode, buf.Bytes()))
	}
	var init struct {
		ReferenceNumber     string
		AuthenticationToken tokenInfo
	}
	if err := jsonUnmarshal(buf.Bytes(), &init); err != nil {
		return err
	}
	op := init.AuthenticationToken.Token
	for range 30 {
		var st struct {
			Status struct {
				Code        int
				Description string
			}
		}
		if err := c.withBearer(ctx, http.MethodGet, "/auth/"+init.ReferenceNumber, op, &st); err != nil {
			return err
		}
		if st.Status.Code == 200 {
			break
		}
		if st.Status.Code >= 400 {
			return fmt.Errorf("xades login: %s", st.Status.Description)
		}
		time.Sleep(time.Second)
	}
	var tok struct{ AccessToken, RefreshToken tokenInfo }
	if err := c.withBearer(ctx, http.MethodPost, "/auth/token/redeem", op, &tok); err != nil {
		return err
	}
	c.accessTok, c.accessUntil, c.refreshTok = tok.AccessToken.Token, tok.AccessToken.ValidUntil, tok.RefreshToken.Token
	return nil
}

// createToken makes a KSeF token with InvoiceRead and InvoiceWrite.
func createToken(ctx context.Context, c *Client) (string, error) {
	var r struct{ ReferenceNumber, Token string }
	if err := c.json(ctx, http.MethodPost, "/tokens", true, map[string]any{
		"permissions": []string{"InvoiceRead", "InvoiceWrite"}, "description": "gfs realsite"}, &r); err != nil {
		return "", err
	}
	for range 30 {
		var st struct{ Status string }
		if err := c.json(ctx, http.MethodGet, "/tokens/"+r.ReferenceNumber, true, nil, &st); err != nil {
			return "", err
		}
		if st.Status == "Active" {
			return r.Token, nil
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("token %s never became active", r.ReferenceNumber)
}

// sendInvoice sends one invoice in an online session and returns its KSeF number.
func sendInvoice(ctx context.Context, c *Client, inv []byte) (string, error) {
	pub, err := c.key(ctx, "SymmetricKeyEncryption")
	if err != nil {
		return "", err
	}
	key, iv := make([]byte, 32), make([]byte, 16)
	rand.Read(key)
	rand.Read(iv)
	encKey, err := encryptOAEP(pub, key)
	if err != nil {
		return "", err
	}
	var sess struct{ ReferenceNumber string }
	if err := c.json(ctx, http.MethodPost, "/sessions/online", true, map[string]any{
		"formCode":   map[string]string{"systemCode": "FA (3)", "schemaVersion": "1-0E", "value": "FA"},
		"encryption": map[string]string{"encryptedSymmetricKey": encKey, "initializationVector": base64.StdEncoding.EncodeToString(iv)},
	}, &sess); err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(inv)%aes.BlockSize
	plain := append(append([]byte(nil), inv...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	b, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(enc, plain)
	var sent struct{ ReferenceNumber string }
	if err := c.json(ctx, http.MethodPost, "/sessions/online/"+sess.ReferenceNumber+"/invoices", true, map[string]any{
		"invoiceHash": b64sha(inv), "invoiceSize": len(inv), "encryptedInvoiceHash": b64sha(enc),
		"encryptedInvoiceSize": len(enc), "encryptedInvoiceContent": base64.StdEncoding.EncodeToString(enc),
	}, &sent); err != nil {
		return "", err
	}
	defer c.json(ctx, http.MethodPost, "/sessions/online/"+sess.ReferenceNumber+"/close", true, nil, nil)
	for range 60 {
		var st struct {
			KsefNumber string
			Status     struct {
				Code        int
				Description string
				Details     []string
			}
		}
		if err := c.json(ctx, http.MethodGet, "/sessions/"+sess.ReferenceNumber+"/invoices/"+sent.ReferenceNumber, true, nil, &st); err != nil {
			return "", err
		}
		if st.Status.Code == 200 {
			return st.KsefNumber, nil
		}
		if st.Status.Code >= 400 {
			return "", fmt.Errorf("invoice refused: %d %s %v", st.Status.Code, st.Status.Description, st.Status.Details)
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("invoice not processed in 2 minutes")
}
