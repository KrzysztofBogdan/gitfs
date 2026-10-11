package jtest

import (
	"strings"
	"testing"
)

func TestBulkMove(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddProject(Project{Key: "GEN", ID: "10017"}, IssueType{ID: "1", Name: "Task"}, IssueType{ID: "2", Name: "Sub-task", Subtask: true})
	s.AddProject(Project{Key: "OPS", ID: "20000"}, IssueType{ID: "1", Name: "Task"}, IssueType{ID: "2", Name: "Sub-task", Subtask: true})
	s.SetFields("OPS", "1", Field{ID: "summary", Name: "Summary", Type: "string", Required: true},
		Field{ID: "components", Name: "Components", Type: "array", Items: "component", Options: []Option{{ID: "9", Value: "api"}}})
	parent := s.AddIssue(Issue{Project: "GEN", Type: "1", Summary: "P", Status: "In Progress",
		Fields: map[string]any{"components": []any{map[string]any{"name": "billing"}}, "labels": []string{"x"}}})
	child := s.AddIssue(Issue{Project: "GEN", Type: "2", Summary: "C", Fields: map[string]any{"parent": map[string]any{"key": parent.Key}}})
	var resp struct{ TaskID string }
	body := map[string]any{"sendBulkNotification": false, "targetToSourcesMapping": map[string]any{
		"20000,1": map[string]any{"inferStatusDefaults": true, "inferFieldDefaults": true, "inferSubtaskTypeDefault": true,
			"issueIdsOrKeys": []string{parent.Key}}}}
	if code := post(t, s, "/rest/api/3/bulk/issues/move", body, &resp); code != 201 || resp.TaskID == "" {
		t.Fatal(code)
	}
	var q struct {
		Status    string
		Processed []int `json:"processedAccessibleIssues"`
	}
	get(t, s, "/rest/api/3/bulk/queue/"+resp.TaskID, &q)
	if q.Status != "COMPLETE" || len(q.Processed) != 2 {
		t.Fatalf("%+v", q)
	}
	p, c := s.Issue(parent.ID), s.Issue(child.ID)
	if p.Project != "OPS" || p.Key != "OPS-1" || p.Status != "In Progress" || p.Fields["components"] != nil || p.Fields["labels"] != nil {
		t.Fatalf("parent %+v", p)
	}
	if c.Project != "OPS" || c.Fields["parent"].(map[string]any)["key"] != "OPS-1" {
		t.Fatalf("child %+v", c)
	}
	var is struct {
		Key    string
		Fields struct{ Status struct{ ID, Name string } }
	}
	if get(t, s, "/rest/api/3/issue/"+parent.Key, &is); is.Key != "OPS-1" || is.Fields.Status.ID != "2" {
		t.Fatalf("old key must resolve, status carries its id: %+v", is)
	}
	// a lone sub-task and an unknown status are refused synchronously
	body["targetToSourcesMapping"] = map[string]any{"20000,2": map[string]any{"inferStatusDefaults": true, "issueIdsOrKeys": []string{c.Key}}}
	if code := post(t, s, "/rest/api/3/bulk/issues/move", body, nil); code != 400 {
		t.Fatal(code)
	}
	body["targetToSourcesMapping"] = map[string]any{"10017,1": map[string]any{"inferStatusDefaults": false,
		"targetStatus": []any{map[string]any{"statuses": map[string]any{"99": []string{"2"}}}}, "issueIdsOrKeys": []string{"OPS-1"}}}
	if code := post(t, s, "/rest/api/3/bulk/issues/move", body, nil); code != 400 || !strings.Contains(strings.Join(s.MoveBodies, ""), "99") {
		t.Fatal(code)
	}
}

func TestMoveTaskStates(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddProject(Project{Key: "GEN", ID: "1"}, IssueType{ID: "1", Name: "Task"})
	s.AddProject(Project{Key: "OPS", ID: "2"}, IssueType{ID: "1", Name: "Task"})
	is := s.AddIssue(Issue{Project: "GEN", Type: "1", Summary: "P"})
	s.MoveRunning, s.MoveFail = 1, "boom"
	var resp struct{ TaskID string }
	post(t, s, "/rest/api/3/bulk/issues/move", map[string]any{"targetToSourcesMapping": map[string]any{
		"2,1": map[string]any{"inferStatusDefaults": true, "issueIdsOrKeys": []string{is.Key}}}}, &resp)
	var q struct{ Status string }
	if get(t, s, "/rest/api/3/bulk/queue/"+resp.TaskID, &q); q.Status != "RUNNING" {
		t.Fatal(q.Status)
	}
	if get(t, s, "/rest/api/3/bulk/queue/"+resp.TaskID, &q); q.Status != "FAILED" || s.Issue(is.ID).Project != "GEN" {
		t.Fatal(q.Status)
	}
}

func TestCreatemetaAllowedValues(t *testing.T) {
	s := New()
	defer s.Close()
	s.AddProject(Project{Key: "GEN"}, IssueType{ID: "1", Name: "Task"})
	s.SetFields("GEN", "1", Field{ID: "customfield_1", Name: "Team", Type: "option", Default: true, Options: []Option{{ID: "7", Value: "Core"}}})
	var resp struct {
		Fields []struct {
			HasDefaultValue bool
			AllowedValues   []struct{ ID, Value, Name string }
		}
	}
	get(t, s, "/rest/api/3/issue/createmeta/GEN/issuetypes/1", &resp)
	if len(resp.Fields) != 1 || !resp.Fields[0].HasDefaultValue || len(resp.Fields[0].AllowedValues) != 1 ||
		resp.Fields[0].AllowedValues[0].Name != "Core" || resp.Fields[0].AllowedValues[0].ID != "7" {
		t.Fatalf("%+v", resp)
	}
}
