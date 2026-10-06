package jira

import (
	"testing"
)

func TestChainOf(t *testing.T) {
	wrap := func(steps string) string {
		return `<issue><status>Closed</status><transitions>` + steps + `</transitions></issue>`
	}
	for x, want := range map[string]string{
		wrap(`<transition to="In Progress"/><transition to="Done"/>`): `<status> Closed does not match the last <transition to="Done">`,
		wrap(`<transition/>`): `<transition> needs to="<status>"`,
		wrap(`<transition to="In Progress"/><transition to="in progress"/><transition to="Closed"/>`): `"in progress" appears twice in <transitions>; split the round trip over two commits`,
		wrap(`<transition to="Closed"><comment>x</comment></transition>`):                             `<comment> cannot go in a <transition>; add it to the issue`,
		wrap(`<transition to="Closed"><foo/></transition>`):                                           `<transition to="Closed">: <foo> is not an issue field`,
		wrap(``): `<transitions> needs at least one <transition to="…">`,
	} {
		if _, err := chainOf(parse(t, x)); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", x, err, want)
		}
	}
	steps, err := chainOf(parse(t, wrap(`<transition to="In Progress"/><transition to="closed" name="Resolve this issue"><resolution>Done</resolution></transition>`)))
	if err != nil || len(steps) != 2 || steps[1].name != "Resolve this issue" || steps[1].node.Child("resolution") == nil {
		t.Fatalf("%+v %v", steps, err)
	}
	if s, err := chainOf(parse(t, `<issue><status>Closed</status></issue>`)); s != nil || err != nil {
		t.Fatal(s, err)
	}
	for cur, want := range map[string]int{"Open": 0, "in progress": 1, "Closed": 2} {
		if got := resumeAt(steps, cur); got != want {
			t.Errorf("resumeAt %s = %d", cur, got)
		}
	}
	root := parse(t, wrap(`<transition to="In Progress"/><transition to="Closed"/>`))
	if got := describeChain(root); got != "transition via In Progress to Closed" {
		t.Fatal(got)
	}
}
