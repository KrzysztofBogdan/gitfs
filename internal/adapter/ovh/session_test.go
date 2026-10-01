package ovh

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// List gives every zone as a stub with its version; Fetch the file with the
// same version.
func TestListFetch(t *testing.T) {
	srv := site(t)
	s := testSession(t, srv, selection{})
	var phases []string
	s.SetProgress(func(p adapter.Progress) { phases = append(phases, p.Phase) })
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	if !l.Full || len(l.Resources) != 2 || l.Resources[0].Path != "domain/zone/a.com.xml" || l.Resources[0].ID != "a.com" ||
		l.Resources[0].Root != nil || l.Resources[0].Version == "" {
		t.Fatalf("%+v", l)
	}
	r, err := s.Fetch(bg, "a.com")
	if err != nil || r.Version != l.Resources[0].Version || r.Path != "domain/zone/a.com.xml" || r.Root.Name != "zone" {
		t.Fatalf("%+v %v", r, err)
	}
	if !strings.Contains(strings.Join(phases, " "), "pages") {
		t.Fatal(phases)
	}
	if _, err := s.Fetch(bg, "nope.com"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
}

// A zone outside the selection is not fetched even by id.
func TestFetchOutsideSelection(t *testing.T) {
	srv := site(t)
	if _, err := testSession(t, srv, selection{zones: []string{"b.pl"}}).Fetch(bg, "a.com"); err == nil {
		t.Fatal("a.com is outside the selection")
	}
}
