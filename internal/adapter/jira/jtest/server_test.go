package jtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func post(t *testing.T, s *Server, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func get(t *testing.T, s *Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(s.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

type searchResp struct {
	Issues []struct {
		ID, Key string
		Fields  map[string]json.RawMessage
	}
	NextPageToken string
	IsLast        bool
}

func fixture() (*Server, time.Time) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	s := New()
	s.Clock = func() time.Time { return now }
	s.AddProject(Project{Key: "GEN"}, IssueType{ID: "1", Name: "Task"})
	s.AddProject(Project{Key: "SUP", Type: "service_desk"}, IssueType{ID: "2", Name: "Support"})
	for i := 0; i < 5; i++ {
		s.AddIssue(Issue{Project: "GEN", Type: "1", Summary: fmt.Sprint("g", i), Updated: now.Add(-time.Duration(i) * time.Hour)})
	}
	s.AddIssue(Issue{Project: "SUP", Type: "2", Summary: "s", Updated: now.Add(-30 * time.Minute)})
	return s, now
}

func TestSearchJQLAndPaging(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	var r searchResp
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project = "GEN" ORDER BY updated DESC`, "maxResults": 2, "fields": []string{"summary"}}, &r)
	if len(r.Issues) != 2 || r.IsLast || r.NextPageToken != "2" || r.Issues[0].Key != "GEN-1" {
		t.Fatalf("%+v", r)
	}
	if _, ok := r.Issues[0].Fields["status"]; ok || string(r.Issues[0].Fields["summary"]) != `"g0"` {
		t.Fatalf("fields not filtered: %v", r.Issues[0].Fields)
	}
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project = "GEN" ORDER BY updated DESC`, "maxResults": 2, "nextPageToken": "4"}, &r)
	if len(r.Issues) != 1 || !r.IsLast || r.Issues[0].Key != "GEN-5" {
		t.Fatalf("%+v", r)
	}
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project in ("GEN","SUP") AND updated >= "-90m" ORDER BY updated ASC`, "maxResults": 100}, &r)
	if len(r.Issues) != 3 || r.Issues[0].Key != "GEN-2" || r.Issues[1].Key != "SUP-1" || r.Issues[2].Key != "GEN-1" {
		t.Fatalf("relative window: %+v", r.Issues)
	}
	var c struct{ Count int }
	post(t, s, "/rest/api/3/search/approximate-count", map[string]any{"jql": `project = "GEN"`}, &c)
	if c.Count != 5 {
		t.Fatal(c)
	}
	if code := post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `assignee = currentUser()`}, nil); code != 400 {
		t.Fatal("unsupported JQL must be refused", code)
	}
}

func TestInlineCapsAndPages(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.WorklogLimit = 2
	for i := 0; i < 3; i++ {
		s.AddWorklog("10003", Worklog{Author: "me", Spent: "1h", Started: "2026-09-29T09:00:00.000+0000"})
	}
	pub := false
	s.AddComment("10008", Comment{Author: "me", Body: json.RawMessage(`{"type":"doc","version":1,"content":[]}`), Public: &pub})
	var r searchResp
	post(t, s, "/rest/api/3/search/jql", map[string]any{"jql": `project in ("GEN","SUP")`, "maxResults": 100, "fields": []string{"worklog", "comment"}}, &r)
	for _, is := range r.Issues {
		var wl struct {
			Worklogs []any
			Total    int
		}
		json.Unmarshal(is.Fields["worklog"], &wl)
		if is.ID == "10003" && (len(wl.Worklogs) != 2 || wl.Total != 3) {
			t.Fatalf("inline worklogs %d of %d", len(wl.Worklogs), wl.Total)
		}
		if is.Key == "SUP-1" && !bytes.Contains(is.Fields["comment"], []byte(`"jsdPublic":false`)) {
			t.Fatalf("JSM comment: %s", is.Fields["comment"])
		}
	}
	var all struct {
		Worklogs []any
		Total    int
	}
	get(t, s, "/rest/api/3/issue/GEN-1/worklog", &all)
	if len(all.Worklogs) != 3 {
		t.Fatal(all)
	}
}

func TestMetaAndProjects(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.SetFields("GEN", "1", Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		Field{ID: "customfield_1", Name: "Team", Type: "string", Custom: "com.atlassian.jira.plugin.system.customfieldtypes:textfield"})
	var ps struct {
		Values []struct{ ID, Key, ProjectTypeKey string }
		IsLast bool
	}
	get(t, s, "/rest/api/3/project/search?startAt=0&maxResults=1", &ps)
	if len(ps.Values) != 1 || ps.IsLast || ps.Values[0].Key != "GEN" {
		t.Fatalf("%+v", ps)
	}
	var ts struct {
		IssueTypes []struct{ ID, Name string }
		Total      int
	}
	get(t, s, "/rest/api/3/issue/createmeta/GEN/issuetypes", &ts)
	var fs struct {
		Fields []struct {
			FieldID  string
			Required bool
			Schema   struct{ Custom string }
		}
	}
	get(t, s, "/rest/api/3/issue/createmeta/GEN/issuetypes/1", &fs)
	if ts.Total != 1 || len(fs.Fields) != 2 || !fs.Fields[0].Required || fs.Fields[1].Schema.Custom == "" {
		t.Fatalf("%+v %+v", ts, fs)
	}
}

func TestRateLimitAndFail(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.RateLimit = 1
	if code := get(t, s, "/rest/api/3/myself", nil); code != 429 {
		t.Fatal(code)
	}
	if code := get(t, s, "/rest/api/3/myself", nil); code != 200 {
		t.Fatal(code)
	}
	s.Fail["GET /rest/api/3/myself"] = 500
	if code := get(t, s, "/rest/api/3/myself", nil); code != 500 {
		t.Fatal(code)
	}
	if s.Count("GET", "/rest/api/3/myself") != 3 {
		t.Fatal(s.Requests)
	}
}

func TestWorkflowEndpoints(t *testing.T) {
	s, _ := fixture()
	defer s.Close()
	s.SetWorkflow("SUP", "2", Workflow{Name: "Support flow",
		Statuses:    []Status{{"10", "Open", "new"}, {"11", "Closed", "done"}},
		Transitions: []Transition{{ID: "5", Name: "Resolve", To: "Closed", From: []string{"Open"}, Screen: []ScreenField{{ID: "resolution", Required: true}}}}})
	if code := get(t, s, "/rest/api/3/workflow/search?workflowName=Support+flow", nil); code != 403 {
		t.Fatal("workflows need admin", code)
	}
	s.Admin = true
	var wf struct {
		Values []struct {
			Transitions []struct {
				Name, To, Type string
				From           []string
			}
		}
	}
	get(t, s, "/rest/api/3/workflow/search?workflowName=Support+flow&expand=transitions,statuses", &wf)
	if tr := wf.Values[0].Transitions[0]; tr.Name != "Resolve" || tr.To != "11" || tr.From[0] != "10" || tr.Type != "directed" {
		t.Fatalf("%+v", wf)
	}
	var st []struct {
		Name     string
		Statuses []struct{ Name string }
	}
	get(t, s, "/rest/api/3/project/SUP/statuses", &st)
	if len(st) != 1 || st[0].Statuses[1].Name != "Closed" {
		t.Fatalf("%+v", st)
	}
}
