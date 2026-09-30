package jira

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func cf(id, name, custom string) fieldMeta {
	return fieldMeta{ID: id, Name: name, Custom: custom}
}

const sys = "com.atlassian.jira.plugin.system.customfieldtypes:"

func TestDecodeField(t *testing.T) {
	cases := []struct {
		m    fieldMeta
		raw  string
		want string
	}{
		{cf("customfield_1", "Team", sys+"textfield"), `"Platform"`, `<field id="customfield_1" name="Team">Platform</field>`},
		{cf("customfield_2", "Story Points", "com.pyxis.greenhopper.jira:jsw-story-points"), `5.0`, `<field id="customfield_2" name="Story Points">5.0</field>`},
		{cf("customfield_3", "Release", sys+"datepicker"), `"2026-10-15"`, `<field id="customfield_3" name="Release">2026-10-15</field>`},
		{cf("customfield_4", "Users", sys+"select"), `{"self":"x","id":"10022","value":"1-10"}`, "<field id=\"customfield_4\" name=\"Users\">\n  <option id=\"10022\">1-10</option>\n</field>"},
		{cf("customfield_5", "Apps", sys+"multicheckboxes"), `[{"id":"1","value":"A"},{"id":"2","value":"B"}]`,
			"<field id=\"customfield_5\" name=\"Apps\">\n  <option id=\"1\">A</option>\n  <option id=\"2\">B</option>\n</field>"},
		{cf("customfield_6", "DRI", sys+"userpicker"), `{"accountId":"712020:a","displayName":"Adam","accountType":"atlassian","active":true}`,
			"<field id=\"customfield_6\" name=\"DRI\">\n  <user account=\"712020:a\">Adam</user>\n</field>"},
		{cf("customfield_7", "Tags", sys+"labels"), `["x","y"]`, "<field id=\"customfield_7\" name=\"Tags\">\n  <label>x</label>\n  <label>y</label>\n</field>"},
		{cf("customfield_8", "Sprint", "com.pyxis.greenhopper.jira:gh-sprint"), `[{"id":42,"name":"Sprint 17","state":"active"}]`,
			"<field id=\"customfield_8\" name=\"Sprint\">\n  <sprint id=\"42\">Sprint 17</sprint>\n</field>"},
		{cf("customfield_9", "License", sys+"textarea"), doc(`{"type":"paragraph","content":[{"type":"text","text":"hi "},{"type":"mention","attrs":{"id":"m1","text":"@Mo"}}]}`),
			"<field id=\"customfield_9\" name=\"License\" type=\"adf\">\n  <paragraph>hi <mention id=\"m1\" text=\"@Mo\"/></paragraph>\n</field>"},
		{cf("customfield_10", "Team", sys+"atlassian-team"), `{"id":"t1","name":"Core"}`, `<field id="customfield_10" name="Team" type="raw">{"id":"t1","name":"Core"}</field>`},
		{cf("customfield_11", "Odd", sys+"textfield"), `{"not":"a string"}`, `<field id="customfield_11" name="Odd" type="raw">{"not":"a string"}</field>`},
		{cf("customfield_12", "App", "ari:cloud:ecosystem::extension/x:static/f"), `"v"`, `<field id="customfield_12" name="App" type="raw">"v"</field>`},
		{fieldMeta{ID: "security", Name: "Security Level", Type: "securitylevel"}, `{"id":"1","name":"Staff"}`, `<field id="security" name="Security Level" type="raw">{"id":"1","name":"Staff"}</field>`},
		{cf("customfield_13", "Empty", sys+"textfield"), `null`, ``},
		{cf("customfield_14", "Empty list", sys+"multiselect"), `[]`, ``},
	}
	reg := newRegistry()
	for _, c := range cases {
		n, err := decodeField(c.m, json.RawMessage(c.raw), reg)
		if err != nil {
			t.Errorf("%s: %v", c.m.ID, err)
			continue
		}
		got := ""
		if n != nil {
			got = xmltree.Print(n, 0)
		}
		if got != c.want {
			t.Errorf("%s: got\n%s\nwant\n%s", c.m.ID, got, c.want)
		}
	}
	if reg.m["712020:a"].Name != "Adam" || reg.m["m1"].Name != "Mo" {
		t.Fatalf("registry %+v", reg.m)
	}
}

func TestRegistry(t *testing.T) {
	r := newRegistry()
	r.see(apiUser{AccountID: "b", DisplayName: "Bo", EmailAddress: "bo@x.com", AccountType: "atlassian", Active: true})
	r.seeMention("a", "@Al")
	r.see(apiUser{AccountID: "a", DisplayName: "Al", AccountType: "customer", Active: true})
	r.seeMention("a", "@Other")    // known: ignored
	r.see(apiUser{AccountID: "b"}) // no new facts
	want := "<people id=\"people\">\n  <person account=\"a\" type=\"customer\" active=\"true\">Al</person>\n  <person account=\"b\" type=\"atlassian\" active=\"true\" email=\"bo@x.com\">Bo</person>\n</people>"
	if got := xmltree.Print(r.node(), 0); got != want {
		t.Fatalf("got\n%s", got)
	}
	if p, ok := r.byEmail("BO@x.com"); !ok || p.Account != "b" {
		t.Fatal(p, ok)
	}
	path := filepath.Join(t.TempDir(), "jira", "people.json")
	if err := r.save(path); err != nil {
		t.Fatal(err)
	}
	r2 := newRegistry()
	if err := r2.load(path); err != nil || r2.resource().Version != r.resource().Version {
		t.Fatalf("reload: %v", err)
	}
	if err := newRegistry().load(filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatal("missing cache must be empty, not an error")
	}
}

func TestFieldSetAndMeta(t *testing.T) {
	tm := &typeMeta{ID: "1", Name: "Task", Fields: map[string]fieldMeta{
		"summary": {ID: "summary"}, "comment": {ID: "comment"}, "attachment": {ID: "attachment"}, "customfield_1": {ID: "customfield_1"},
	}}
	got := fieldSet(tm)
	if _, ok := got["comment"]; ok || len(got) != 3 || got["environment"].ID != "environment" {
		t.Fatalf("%+v", got)
	}
	path := filepath.Join(t.TempDir(), "meta.json")
	m, _ := loadMeta(path)
	m.Projects["GEN"] = &projectMeta{Key: "GEN", ID: "10017", Types: map[string]*typeMeta{"1": tm}}
	if err := m.save(path); err != nil {
		t.Fatal(err)
	}
	m2, err := loadMeta(path)
	if err != nil || m2.Projects["GEN"].typeNamed("task") == nil || m2.Projects["GEN"].dir() != "gen" {
		t.Fatalf("%+v %v", m2, err)
	}
	os.WriteFile(path, []byte("{broken"), 0o644)
	if m3, err := loadMeta(path); err != nil || len(m3.Projects) != 0 {
		t.Fatal("a broken cache must load empty")
	}
}
