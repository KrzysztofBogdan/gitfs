package confluence

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want, err string }{
		{"site", "confluence://acme.atlassian.net", "confluence://acme.atlassian.net", ""},
		{"site slash", "confluence://acme.atlassian.net/", "confluence://acme.atlassian.net", ""},
		{"path key", "confluence://acme.atlassian.net/HF", "confluence://acme.atlassian.net?filter=HF", ""},
		{"personal key", "confluence://acme.atlassian.net/~jan", "confluence://acme.atlassian.net?filter=~jan", ""},
		{"user and params", "confluence://me%40x.com@acme.atlassian.net?exclude=OLD,~jan&type=all", "confluence://me%40x.com@acme.atlassian.net?type=all&exclude=OLD,~jan", ""},
		{"filter list", "confluence://acme.atlassian.net?filter=HF,ENG", "confluence://acme.atlassian.net?filter=HF,ENG", ""},
		{"base kept", "confluence://acme.atlassian.net/HF?base=http://127.0.0.1:9", "confluence://acme.atlassian.net?base=http%3A%2F%2F127.0.0.1%3A9&filter=HF", ""},
		{"browser page", "confluence:https://acme.atlassian.net/wiki/spaces/HF/pages/123/Title", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser overview", "confluence:https://acme.atlassian.net/wiki/spaces/HF/overview", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser with query and fragment", "confluence:https://acme.atlassian.net/wiki/spaces/HF/pages/1/T?atlOrigin=abc#Section", "confluence://acme.atlassian.net?filter=HF", ""},
		{"browser personal", "confluence:https://acme.atlassian.net/wiki/spaces/~jan/overview", "confluence://acme.atlassian.net?filter=~jan", ""},
		{"browser site", "confluence:https://acme.atlassian.net/wiki/home", "confluence://acme.atlassian.net", ""},
		{"browser not wiki", "confluence:https://acme.atlassian.net/jira/x", "", "want confluence://<site>"},
		{"deep path", "confluence://acme.atlassian.net/a/b", "", "want confluence://<site>"},
		{"wiki path", "confluence://acme.atlassian.net/wiki/HF", "", "want confluence://<site>"},
		{"no host", "confluence:///HF", "", "want confluence://<site>"},
		{"key twice", "confluence://acme.atlassian.net/HF?filter=ENG", "", "not both"},
		{"unknown param", "confluence://acme.atlassian.net?filtr=HF", "", `unknown parameter "filtr"`},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := normalize(u)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%s: got %v %v, want %s", c.name, got, err, c.want)
		}
	}
}

func TestParseSelection(t *testing.T) {
	cases := []struct {
		q    string
		want selection
		err  string
	}{
		{"", selection{}, ""},
		{"type=collaboration", selection{typ: "collaboration"}, ""},
		{"type=knowledge_base", selection{typ: "knowledge_base"}, ""},
		{"type=personal", selection{typ: "personal"}, ""},
		{"type=all&exclude=OLD,~jan", selection{typ: "all", exclude: []string{"OLD", "~jan"}}, ""},
		{"filter=HF,ENG", selection{keys: []string{"HF", "ENG"}}, ""},
		{"filter=HF&exclude=HF", selection{keys: []string{"HF"}, exclude: []string{"HF"}}, ""},
		{"filter=HF&type=global", selection{}, "cannot be combined"},
		{"type=team", selection{}, "want global, collaboration, knowledge_base, personal or all"},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		got, err := parseSelection(q)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.q, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v %v", c.q, got, err)
		}
	}
}

func TestAdapterNormalizeAndDefaultDir(t *testing.T) {
	a := &Adapter{}
	cases := []struct{ in, norm, dir string }{
		{"confluence://acme.atlassian.net/HF", "confluence://acme.atlassian.net?filter=HF", "hf"},
		{"confluence:https://acme.atlassian.net/wiki/spaces/~jan/overview", "confluence://acme.atlassian.net?filter=~jan", "~jan"},
		{"confluence://acme.atlassian.net", "confluence://acme.atlassian.net", "acme"},
		{"confluence://acme.atlassian.net?filter=HF,ENG", "confluence://acme.atlassian.net?filter=HF,ENG", "acme"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.in)
		got, err := a.Normalize(u)
		if err != nil || got != c.norm {
			t.Errorf("%s: Normalize %q %v", c.in, got, err)
		}
		if d := a.DefaultDir(u); d != c.dir {
			t.Errorf("%s: DefaultDir %q, want %q", c.in, d, c.dir)
		}
	}
	u, _ := url.Parse("confluence://acme.atlassian.net?filter=HF&type=all")
	if _, err := a.Normalize(u); err == nil {
		t.Fatal("type with filter must fail Normalize")
	}
}
