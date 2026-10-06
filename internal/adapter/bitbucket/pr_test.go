package bitbucket

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const prJSON = `{"id":142,"title":"Add rate limiting","description":"Markdown *text*.","state":"OPEN","draft":false,
 "author":{"account_id":"712020:aa","display_name":"Ana Nowak"},
 "source":{"branch":{"name":"feat/rate-limit"},"commit":{"hash":"a1b2c3d4e5f6"}},
 "destination":{"branch":{"name":"main","merge_strategies":["merge_commit","squash"],"default_merge_strategy":"squash"},"commit":{"hash":"0f9e8d7c6b5a"}},
 "merge_commit":null,"close_source_branch":true,
 "reviewers":[{"account_id":"712020:bb","display_name":"Bo Smith"},{"account_id":"712020:cc","display_name":"Cy Lee"}],
 "participants":[{"user":{"account_id":"712020:bb","display_name":"Bo Smith"},"role":"REVIEWER","approved":true,"state":"approved"},
  {"user":{"account_id":"712020:cc","display_name":"Cy Lee"},"role":"REVIEWER","approved":false,"state":"changes_requested"},
  {"user":{"account_id":"712020:me","display_name":"Me Myself"},"role":"PARTICIPANT","approved":true,"state":null}],
 "created_on":"2026-10-01T09:12:00.123456+00:00","updated_on":"2026-10-05T14:22:10.5+00:00"}`

const commentsJSON = `[
 {"id":9012,"content":{"raw":"General remark"},"user":{"account_id":"712020:bb","display_name":"Bo Smith"},"created_on":"2026-10-02T00:00:00+00:00","updated_on":"2026-10-02T00:00:00+00:00","deleted":false},
 {"id":9013,"content":{"raw":"Off by one?"},"user":{"account_id":"712020:cc","display_name":"Cy Lee"},"inline":{"path":"src/limit.go","from":null,"to":42},"resolution":{"type":"comment_resolution"},"created_on":"2026-10-03T00:00:00+00:00","updated_on":"2026-10-03T00:00:00+00:00"},
 {"id":9014,"content":{"raw":"old side"},"user":{"account_id":"712020:cc","display_name":"Cy Lee"},"inline":{"path":"src/limit.go","from":7,"to":null},"created_on":"2026-10-03T01:00:00+00:00","updated_on":"2026-10-03T01:00:00+00:00"},
 {"id":9020,"content":{"raw":"Fixed."},"user":{"account_id":"712020:aa","display_name":"Ana Nowak"},"parent":{"id":9013},"created_on":"2026-10-04T00:00:00+00:00","updated_on":"2026-10-04T00:00:00+00:00"},
 {"id":9021,"content":{"raw":""},"user":{"account_id":"712020:aa","display_name":"Ana Nowak"},"deleted":true,"created_on":"2026-10-04T01:00:00+00:00","updated_on":"2026-10-04T01:00:00+00:00"}]`

const wantPR = `<pullrequest id="api/pr/142" number="142" created="2026-10-01T09:12:00Z" updated="2026-10-05T14:22:10Z" version="VERSION">
  <title>Add rate limiting</title>
  <state>open</state>
  <draft>false</draft>
  <source branch="feat/rate-limit" commit="a1b2c3d4e5f6"/>
  <destination branch="main" commit="0f9e8d7c6b5a"/>
  <close-source-branch>true</close-source-branch>
  <author account="712020:aa">Ana Nowak</author>
  <reviewer account="712020:bb" state="approved">Bo Smith</reviewer>
  <reviewer account="712020:cc" state="changes_requested">Cy Lee</reviewer>
  <participant account="712020:me" state="approved">Me Myself</participant>
  <my-review>approved</my-review>
  <description>Markdown *text*.</description>
  <build key="pipe-1" state="FAILED" url="https://bitbucket.org/acme/api/pipelines/results/1873" run="../pipelines/1873.xml">Pipeline #1873</build>
  <build key="sonar" state="SUCCESSFUL" url="https://sonar.example/x">Sonar</build>
  <comment id="9012" account="712020:bb" author="Bo Smith" created="2026-10-02T00:00:00Z" updated="2026-10-02T00:00:00Z" resolved="false">General remark</comment>
  <comment id="9013" account="712020:cc" author="Cy Lee" created="2026-10-03T00:00:00Z" updated="2026-10-03T00:00:00Z" path="src/limit.go" line="42" side="new" resolved="true">Off by one?</comment>
  <comment id="9014" account="712020:cc" author="Cy Lee" created="2026-10-03T01:00:00Z" updated="2026-10-03T01:00:00Z" path="src/limit.go" line="7" side="old" resolved="false">old side</comment>
  <comment id="9020" account="712020:aa" author="Ana Nowak" created="2026-10-04T00:00:00Z" updated="2026-10-04T00:00:00Z" parent="9013">Fixed.</comment>
</pullrequest>`

func prFixture(t *testing.T) prInput {
	t.Helper()
	in := prInput{Repo: "api", Me: "712020:me", Statuses: []apiStatus{
		{Key: "pipe-1", State: "FAILED", Name: "Pipeline #1873", URL: "https://bitbucket.org/acme/api/pipelines/results/1873"},
		{Key: "sonar", State: "SUCCESSFUL", Name: "Sonar", URL: "https://sonar.example/x"}}}
	if err := json.Unmarshal([]byte(prJSON), &in.PR); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(commentsJSON), &in.Comments); err != nil {
		t.Fatal(err)
	}
	return in
}

func canonical(t *testing.T, n *xmltree.Node) string {
	t.Helper()
	c := n.Clone()
	canon.Normalize(c, mergedSchema)
	return xmltree.Print(c, 0)
}

func TestPRRoot(t *testing.T) {
	in := prFixture(t)
	got := prRoot(in)
	want, err := xmltree.ParseString(strings.Replace(wantPR, "VERSION", prVersion(in), 1))
	if err != nil {
		t.Fatal(err)
	}
	if g, w := canonical(t, got), canonical(t, want); g != w {
		t.Fatalf("got\n%s\nwant\n%s", g, w)
	}
	if err := validate.Resource(got, got.Clone(), mergedSchema); err != nil {
		t.Fatal(err)
	}
}

func TestPRVersion(t *testing.T) {
	in := prFixture(t)
	v := prVersion(in)
	if !strings.HasPrefix(v, "2026-10-05T14:22:10Z+") || prVersion(prFixture(t)) != v {
		t.Fatal(v)
	}
	in.Statuses[0].State = "SUCCESSFUL"
	if prVersion(in) == v {
		t.Fatal("a status change moves the version")
	}
}

func TestMyReview(t *testing.T) {
	in := prFixture(t)
	in.Me = "712020:cc"
	if got := text(prRoot(in).Child("my-review")); got != "changes_requested" {
		t.Fatal(got)
	}
	in.Me = "712020:nobody"
	if got := text(prRoot(in).Child("my-review")); got != "none" {
		t.Fatal(got)
	}
}

func TestPaths(t *testing.T) {
	if got := prPath("api", 142, "Add: rate/limit"); got != "api/pr/142 Add: rate-limit.xml" {
		t.Fatal(got)
	}
	if got := prPath("api", 1, strings.Repeat("é", 200)); len(got) > len("api/pr/")+maxName+len(".xml") {
		t.Fatal(len(got))
	}
	if r, n, ok := parsePRID(prID("api", 7)); !ok || r != "api" || n != 7 {
		t.Fatal(r, n)
	}
	if _, _, ok := parsePRID("api/pipelines/x"); ok {
		t.Fatal("run id is not a PR id")
	}
	if r, u, ok := parseRunID(runID("api", "{abc}")); !ok || r != "api" || u != "abc" {
		t.Fatal(r, u)
	}
	if runPath("api", 9) != "api/pipelines/9.xml" || logName(1, "Build & Test") != "1-build-test.log" {
		t.Fatal("paths")
	}
}
