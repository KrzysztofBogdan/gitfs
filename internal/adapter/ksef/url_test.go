package ksef

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestValidNIP(t *testing.T) {
	for _, s := range []string{"6887603807", "5261040828"} {
		if !validNIP(s) {
			t.Error(s)
		}
	}
	for _, s := range []string{"6887603808", "123", "abcdefghij", "68876038070"} {
		if validNIP(s) {
			t.Error(s)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"ksef://KSeF.mf.gov.pl/6887603807":                                   "ksef://ksef.mf.gov.pl/6887603807",
		"ksef://test.ksef.mf.gov.pl/6887603807/":                             "ksef://test.ksef.mf.gov.pl/6887603807",
		"ksef://ksef.mf.gov.pl/6887603807?roles=received,issued":             "ksef://ksef.mf.gov.pl/6887603807?roles=issued,received",
		"ksef://ksef.mf.gov.pl/6887603807?roles=third-party,issued,received": "ksef://ksef.mf.gov.pl/6887603807",
		"ksef://ksef.mf.gov.pl/6887603807?since=-90d&roles=received":         "ksef://ksef.mf.gov.pl/6887603807?since=-90d&roles=received",
		"ksef://ksef.mf.gov.pl/6887603807?since=2026-02-01":                  "ksef://ksef.mf.gov.pl/6887603807",
	} {
		n, err := normalize(mustURL(t, in))
		if err != nil || n.String() != want {
			t.Errorf("%s: %v %v, want %s", in, n, err, want)
		}
	}
	for _, in := range []string{
		"ksef://example.com/6887603807", "ksef://ksef.mf.gov.pl/6887603808", "ksef://ksef.mf.gov.pl/",
		"ksef://ksef.mf.gov.pl/6887603807?roles=authorized", "ksef://ksef.mf.gov.pl/6887603807?since=yesterday",
		"ksef://ksef.mf.gov.pl/6887603807?limit=5", "ksef://ksef.mf.gov.pl/6887603807/x",
	} {
		if _, err := normalize(mustURL(t, in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	for in, want := range map[string]string{
		"ksef://ksef.mf.gov.pl/6887603807":      "ksef-6887603807",
		"ksef://test.ksef.mf.gov.pl/6887603807": "ksef-test-6887603807",
		"ksef://demo.ksef.mf.gov.pl/6887603807": "ksef-demo-6887603807",
	} {
		if got := defaultDir(mustURL(t, in)); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestSinceTime(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"":           time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		"2025-03-04": time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC),
		"-10d":       now.AddDate(0, 0, -10),
		"-2w":        now.AddDate(0, 0, -14),
		"-6m":        now.AddDate(0, -6, 0),
	} {
		got, err := sinceTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
}

type entries map[string]string

func (e entries) GetEntry(n string) (string, error) {
	if v, ok := e[n]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestParseTarget(t *testing.T) {
	u := mustURL(t, "ksef://test.ksef.mf.gov.pl/6887603807?roles=received")
	env := func(string) string { return "" }
	if _, err := parseTarget(u, env, entries{}); err == nil || !strings.Contains(err.Error(), "run gfs auth login ksef://test.ksef.mf.gov.pl/6887603807") {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, env, entries{"ksef:test.ksef.mf.gov.pl/6887603807": "tok"})
	if err != nil || tg.token != "tok" || tg.base != "https://api-test.ksef.mf.gov.pl/v2" || len(tg.roles) != 1 || tg.roles[0] != "received" {
		t.Fatalf("%+v %v", tg, err)
	}
	tg, _ = parseTarget(u, func(k string) string { return map[string]string{"GFS_KSEF_TOKEN": "envtok"}[k] }, entries{})
	if tg.token != "envtok" {
		t.Fatal(tg.token)
	}
}

func TestPDFParameter(t *testing.T) {
	for in, want := range map[string]string{
		"ksef://ksef.mf.gov.pl/6887603807?pdf=yes": "ksef://ksef.mf.gov.pl/6887603807",
		"ksef://ksef.mf.gov.pl/6887603807?pdf=no":  "ksef://ksef.mf.gov.pl/6887603807?pdf=no",
	} {
		u, _ := url.Parse(in)
		n, err := normalize(u)
		if err != nil || n.String() != want {
			t.Errorf("%s: %v %v", in, n, err)
		}
	}
	u, _ := url.Parse("ksef://ksef.mf.gov.pl/6887603807?pdf=maybe")
	if _, err := normalize(u); err == nil || !strings.Contains(err.Error(), "pdf=maybe: want yes or no") {
		t.Fatal(err)
	}
	store := entries{}
	env := func(string) string { return "tok" }
	for raw, want := range map[string]bool{"ksef://test.ksef.mf.gov.pl/6887603807": true, "ksef://test.ksef.mf.gov.pl/6887603807?pdf=no": false} {
		u, _ := url.Parse(raw)
		tg, err := parseTarget(u, env, store)
		if err != nil || tg.pdf != want {
			t.Errorf("%s: pdf=%v %v", raw, tg.pdf, err)
		}
	}
	if qrBase["test.ksef.mf.gov.pl"] != "https://qr-test.ksef.mf.gov.pl" || qrBase["ksef.mf.gov.pl"] != "https://qr.ksef.mf.gov.pl" || qrBase["demo.ksef.mf.gov.pl"] != "https://qr-demo.ksef.mf.gov.pl" {
		t.Fatal(qrBase)
	}
}
