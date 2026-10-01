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
		{"delete", nil, nil, false, "needs confirmation: rerun with --allow delete or --force"},
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

func TestReplyAndApproveAsk(t *testing.T) {
	p, _ := FromConfig(nil)
	if p.Level("reply") != Ask || p.Level("approve") != Ask || p.Level("comment") != Allow {
		t.Fatal(p.Level("reply"), p.Level("approve"), p.Level("comment"))
	}
}

// Force (gfs commit --force) turns every ask into allow; deny still wins.
func TestForce(t *testing.T) {
	p, _ := FromConfig(map[string]string{"update": "deny"})
	d := &Decider{Policy: p, Force: true}
	if run, reason := d.Decide("delete", "q?"); !run || reason != "" {
		t.Fatalf("forced ask: %v %q", run, reason)
	}
	if run, _ := d.Decide("update", "q?"); run {
		t.Fatal("deny must win over --force")
	}
	if d.Asks("delete") || !(&Decider{Policy: p}).Asks("delete") {
		t.Fatal("Asks must reflect --force")
	}
	if reason := func() string { _, r := (&Decider{Policy: p}).Decide("delete", "q?"); return r }(); reason != "needs confirmation: rerun with --allow delete or --force" {
		t.Fatal(reason)
	}
}

func TestDNSAsks(t *testing.T) {
	p, _ := FromConfig(nil)
	if p.Level("dns") != Ask {
		t.Fatal(p.Level("dns"))
	}
}
