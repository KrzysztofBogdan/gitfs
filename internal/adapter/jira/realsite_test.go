package jira

// Checks against a real Jira Cloud site (jira spec §11.4). They skip unless
// the environment names a site:
//
//	GFS_JIRA_SITE=jira://<site>/<PROJECT>   read: ADF round trip on up to 30 issues
//	GFS_JIRA_SCRATCH=<KEY>                  write: a project where tests may create and delete issues
//	GFS_JIRA_SCRATCH_JSM=<KEY>              write: a service project for the internal-comment check
//	GFS_JIRA_CUSTOMER=jira+customer://<site> customer: list and fetch
//
// plus GFS_JIRA_EMAIL and GFS_JIRA_TOKEN (or a stored token). Example:
//
//	GFS_JIRA_SITE=jira://acme.atlassian.net/GEN go test ./internal/adapter/jira -run Real -v

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/creds"
)

func realSession(t *testing.T, raw string) *session {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// normalADF is v as gfs would give it back: null attributes dropped,
// adjacent text with the same marks merged, marks in wrapper order.
func normalADF(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if val == nil {
				continue
			}
			if k == "attrs" {
				attrs := map[string]any{}
				for ak, av := range val.(map[string]any) {
					if av != nil {
						attrs[ak] = av
					}
				}
				if len(attrs) == 0 {
					continue
				}
				val = attrs
			}
			out[k] = normalADF(val)
		}
		if ms, ok := out["marks"].([]any); ok {
			sort.SliceStable(ms, func(i, j int) bool {
				return markRank[ms[i].(map[string]any)["type"].(string)] < markRank[ms[j].(map[string]any)["type"].(string)]
			})
		}
		if c, ok := out["content"].([]any); ok {
			var merged []any
			for _, item := range c {
				merged = appendMerged(merged, item)
			}
			if len(merged) == 0 {
				delete(out, "content")
			} else {
				out["content"] = merged
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalADF(x[i])
		}
		return out
	}
	return v
}

func TestRealADFRoundTrip(t *testing.T) {
	raw := os.Getenv("GFS_JIRA_SITE")
	if raw == "" {
		t.Skip("GFS_JIRA_SITE not set")
	}
	s := realSession(t, raw+map[bool]string{true: "&", false: "?"}[strings.Contains(raw, "?")]+"limit=30")
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range l.Resources {
		if r.ID == peopleID || r.ID == workflowsID {
			continue
		}
		is, err := s.getIssue(bg, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"description", "environment"} {
			if isEmptyJSON(is.Fields[f]) {
				continue
			}
			nodes, err := adfToNodes(is.Fields[f])
			if err != nil {
				t.Errorf("%s %s: %v", is.Key, f, err)
				continue
			}
			back, err := nodesToADF(nodes)
			if err != nil {
				t.Errorf("%s %s back: %v", is.Key, f, err)
				continue
			}
			var orig any
			json.Unmarshal(is.Fields[f], &orig)
			if m, ok := normalADF(orig).(map[string]any); ok && m["content"] == nil {
				orig = nil // an empty doc: gfs shows no element and never sends one
			}
			b, _ := json.Marshal(back)
			var got any
			json.Unmarshal(b, &got)
			if !reflect.DeepEqual(normalADF(orig), normalADF(got)) {
				ob, _ := json.Marshal(normalADF(orig))
				gb, _ := json.Marshal(normalADF(got))
				t.Errorf("%s %s differs\nremote %s\ngfs    %s", is.Key, f, ob, gb)
			}
			checked++
		}
	}
	t.Logf("%d bodies round-tripped", checked)
}

func updatedOf(t *testing.T, s *session, id string) string {
	t.Helper()
	var is apiIssue
	if err := s.c.Do(bg, http.MethodGet, "/rest/api/3/issue/"+id+"?fields=updated", nil, &is); err != nil {
		t.Fatal(err)
	}
	return is.str("updated")
}

// scratchIssue creates an issue in the scratch project and deletes it when the test ends.
func scratchIssue(t *testing.T, s *session, key, summary string) (id, issueKey string) {
	t.Helper()
	p := s.projects[key]
	if p == nil {
		t.Fatalf("scratch project %s not visible", key)
	}
	if err := s.loadTypes(bg, p); err != nil {
		t.Fatal(err)
	}
	var typeID string
	for _, tm := range p.Types {
		if !tm.Subtask {
			typeID = tm.ID
			break
		}
	}
	var resp struct{ ID, Key string }
	body := map[string]any{"fields": map[string]any{"project": map[string]any{"key": key}, "issuetype": map[string]any{"id": typeID}, "summary": summary}}
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue", body, &resp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.c.Do(bg, http.MethodDelete, "/rest/api/3/issue/"+resp.ID, nil, nil) })
	return resp.ID, resp.Key
}

func TestRealWrites(t *testing.T) {
	key := os.Getenv("GFS_JIRA_SCRATCH")
	site := os.Getenv("GFS_JIRA_SITE")
	if key == "" || site == "" {
		t.Skip("GFS_JIRA_SITE and GFS_JIRA_SCRATCH not set")
	}
	u, _ := url.Parse(site)
	s := realSession(t, "jira://"+u.Host+"/"+key)
	a, akey := scratchIssue(t, s, key, "gfs real-site check A")
	_, bkey := scratchIssue(t, s, key, "gfs real-site check B")
	ic := &issueCtx{id: a, key: akey, p: s.projects[key]}

	before := updatedOf(t, s, a)
	time.Sleep(1100 * time.Millisecond)
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue/"+a+"/comment", map[string]any{"body": map[string]any{"type": "doc", "version": 1,
		"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "gfs check"}}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if after := updatedOf(t, s, a); after == before {
		t.Error("adding a comment must bump the issue's updated (incremental pull relies on it)")
	}
	before = updatedOf(t, s, a)
	time.Sleep(1100 * time.Millisecond)
	if err := s.c.Do(bg, http.MethodPost, "/rest/api/3/issue/"+a+"/worklog?adjustEstimate=auto",
		map[string]any{"started": time.Now().Format("2006-01-02T15:04:05.000-0700"), "timeSpent": "1m"}, nil); err != nil {
		t.Fatal(err)
	}
	if after := updatedOf(t, s, a); after == before {
		t.Error("adding a worklog must bump the issue's updated")
	}

	lts, err := s.loadLinkTypes(bg)
	if err != nil || len(lts) == 0 {
		t.Fatal(err)
	}
	lt := lts[0]
	req := adapter.ApplyRequest{Local: &adapter.Resource{Root: el("issue")}}
	req.Local.Root.Children = append(req.Local.Root.Children, textEl2("link", bkey, "type", lt.Outward))
	if err := s.sub(bg, ic, req, adapter.Action{Verb: "create", Target: "link[1]"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Fetch(bg, a)
	if err != nil {
		t.Fatal(err)
	}
	l := r.Root.Child("link")
	if typ, _ := attr(l, "type"); l == nil || typ != lt.Outward || textOf(l) != bkey {
		t.Errorf("link direction: wrote \"%s %s %s\", Jira shows %q on %s; swap the keys in linkBody", akey, lt.Outward, bkey, typ+" "+textOf(l), akey)
	}
}

func TestRealInternalComment(t *testing.T) {
	key := os.Getenv("GFS_JIRA_SCRATCH_JSM")
	site := os.Getenv("GFS_JIRA_SITE")
	if key == "" || site == "" {
		t.Skip("GFS_JIRA_SITE and GFS_JIRA_SCRATCH_JSM not set")
	}
	u, _ := url.Parse(site)
	s := realSession(t, "jira://"+u.Host+"/"+key)
	id, k := scratchIssue(t, s, key, "gfs internal comment check")
	ic := &issueCtx{id: id, key: k, p: s.projects[key]}
	root := el("issue")
	c := el("comment", "internal", "true")
	c.Children = append(c.Children, textEl("paragraph", "internal gfs check"))
	root.Children = append(root.Children, c)
	if err := s.sub(bg, ic, adapter.ApplyRequest{Local: &adapter.Resource{Root: root}}, adapter.Action{Verb: "create", Target: "comment[1]"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := attr(r.Root.Child("comment"), "internal"); v != "true" {
		t.Fatalf("the comment must stay internal:\n%v", r.Root.Child("comment"))
	}
}

func TestRealCustomer(t *testing.T) {
	raw := os.Getenv("GFS_JIRA_CUSTOMER")
	if raw == "" {
		t.Skip("GFS_JIRA_CUSTOMER not set")
	}
	u, _ := url.Parse(raw)
	tg, err := parseTarget(u, nil, os.Getenv, creds.System{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := openCustomer(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Resources {
		if r.ID == peopleID {
			continue
		}
		if _, err := s.Fetch(bg, r.ID); err != nil {
			t.Fatalf("%s: %v", r.Path, err)
		}
		t.Logf("%s", r.Path)
		break
	}
	t.Logf("%d requests", len(l.Resources)-1)
}
