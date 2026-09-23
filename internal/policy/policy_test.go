package policy

import "testing"

func TestDecide(t *testing.T) {
	p, err := FromConfig(map[string]string{"update": "deny", "publish": "allow"})
	if err != nil {
		t.Fatal(err)
	}
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	cases := []struct {
		class   string
		allowed map[string]bool
		prompt  func(string) bool
		run     bool
		reason  string
	}{
		{"create", nil, nil, true, ""},
		{"update", nil, yes, false, "denied by policy"},
		{"update", map[string]bool{"update": true}, yes, false, "denied by policy"},
		{"publish", nil, nil, true, ""},
		{"delete", nil, nil, false, "needs confirmation: rerun with --allow delete"},
		{"delete", map[string]bool{"delete": true}, nil, true, ""},
		{"send", nil, yes, true, ""},
		{"send", nil, no, false, "declined"},
	}
	for _, c := range cases {
		d := &Decider{Policy: p, Allowed: c.allowed, Prompt: c.prompt}
		run, reason := d.Decide(c.class, "q?")
		if run != c.run || reason != c.reason {
			t.Errorf("%s: got %v %q, want %v %q", c.class, run, reason, c.run, c.reason)
		}
	}
	if _, err := FromConfig(map[string]string{"send": "maybe"}); err == nil {
		t.Fatal("want error for bad level")
	}
}
