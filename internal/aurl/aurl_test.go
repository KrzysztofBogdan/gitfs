package aurl

import "testing"

func TestParseValid(t *testing.T) {
	cases := []struct {
		in      string
		scheme  string
		account string
		path    string
		query   map[string]string
	}{
		{"jira://acme/PROJ", "jira", "acme", "PROJ", map[string]string{}},
		{"jira://acme/PROJ-1234", "jira", "acme", "PROJ-1234", map[string]string{}},
		{"imap://me@example.com", "imap", "me@example.com", "", map[string]string{}},
		{"imap://me@foo.com?host=imap.foo.com:993", "imap", "me@foo.com", "",
			map[string]string{"host": "imap.foo.com:993"}},
		{"domains://", "domains", "", "", map[string]string{}},
		{"slack://acme-ws/channel-name", "slack", "acme-ws", "channel-name", map[string]string{}},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.in, err)
		}
		if got.Scheme != c.scheme || got.Account != c.account || got.Path != c.path {
			t.Errorf("Parse(%q) = {%s, %s, %s}; want {%s, %s, %s}",
				c.in, got.Scheme, got.Account, got.Path,
				c.scheme, c.account, c.path)
		}
		for k, v := range c.query {
			if got.Query[k] != v {
				t.Errorf("Parse(%q) query[%s] = %q; want %q",
					c.in, k, got.Query[k], v)
			}
		}
		if got.Raw != c.in {
			t.Errorf("Raw = %q; want %q", got.Raw, c.in)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []string{
		"",
		"no-scheme",
		"JIRA://acme/PROJ", // uppercase scheme
		"://empty-scheme",
		"Jira://acme/PROJ",
	}
	for _, in := range cases {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) expected error, got nil", in)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{"acme", "PROJ"}, "acme-proj"},
		{[]string{"me@gmail.com", ""}, "me-gmail-com"},
		{[]string{"", ""}, ""},
		{[]string{"acme-workspace", "channel-name"}, "acme-workspace-channel-name"},
	}
	for _, c := range cases {
		if got := Slugify(c.parts...); got != c.want {
			t.Errorf("Slugify(%v) = %q; want %q", c.parts, got, c.want)
		}
	}
}
