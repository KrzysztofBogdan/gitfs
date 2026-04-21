package imap

import (
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
)

func mkPlan(uid uint32, sub string, date time.Time) *plan {
	return &plan{UID: goimap.UID(uid), Date: date, SanitizedSubject: sub}
}

func TestAssignFilenamesUniqueSubject(t *testing.T) {
	p := mkPlan(1, "foo", time.Unix(100, 0))
	assignFilenames([]*plan{p})
	if p.Filename != "foo.md" {
		t.Errorf("got %q; want foo.md", p.Filename)
	}
}

func TestAssignFilenamesSameSubjectDifferentDates(t *testing.T) {
	// Newer message first in input — output should still number by date asc.
	older := mkPlan(5, "foo", time.Unix(100, 0))
	newer := mkPlan(3, "foo", time.Unix(200, 0))
	assignFilenames([]*plan{newer, older})
	if older.Filename != "foo.md" {
		t.Errorf("oldest should be foo.md, got %q", older.Filename)
	}
	if newer.Filename != "foo-2.md" {
		t.Errorf("newer should be foo-2.md, got %q", newer.Filename)
	}
}

func TestAssignFilenamesSameSubjectSameDateTiebreakByUID(t *testing.T) {
	d := time.Unix(100, 0)
	hi := mkPlan(10, "foo", d)
	lo := mkPlan(2, "foo", d)
	assignFilenames([]*plan{hi, lo})
	if lo.Filename != "foo.md" {
		t.Errorf("lower UID should be foo.md, got %q", lo.Filename)
	}
	if hi.Filename != "foo-2.md" {
		t.Errorf("higher UID should be foo-2.md, got %q", hi.Filename)
	}
}

func TestAssignFilenamesEmptySubjects(t *testing.T) {
	a := mkPlan(1, "no-subject", time.Unix(100, 0))
	b := mkPlan(2, "no-subject", time.Unix(200, 0))
	c := mkPlan(3, "no-subject", time.Unix(300, 0))
	assignFilenames([]*plan{a, b, c})
	if a.Filename != "no-subject.md" {
		t.Errorf("a: got %q", a.Filename)
	}
	if b.Filename != "no-subject-2.md" {
		t.Errorf("b: got %q", b.Filename)
	}
	if c.Filename != "no-subject-3.md" {
		t.Errorf("c: got %q", c.Filename)
	}
}

func TestAssignFilenamesMixedGroupsNumberedIndependently(t *testing.T) {
	f1 := mkPlan(1, "foo", time.Unix(100, 0))
	f2 := mkPlan(2, "foo", time.Unix(200, 0))
	b1 := mkPlan(3, "bar", time.Unix(150, 0))
	assignFilenames([]*plan{f1, b1, f2})
	if f1.Filename != "foo.md" || f2.Filename != "foo-2.md" {
		t.Errorf("foo group: %q, %q", f1.Filename, f2.Filename)
	}
	if b1.Filename != "bar.md" {
		t.Errorf("bar group: %q", b1.Filename)
	}
}
