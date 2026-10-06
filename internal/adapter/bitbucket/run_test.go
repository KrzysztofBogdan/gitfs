package bitbucket

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/validate"
)

const runJSON = `{"uuid":"{0b1c}","build_number":1873,"creator":{"account_id":"712020:aa","display_name":"Ana Nowak"},
 "state":{"name":"COMPLETED","result":{"name":"FAILED"}},
 "target":{"type":"pipeline_pullrequest_target","source":"feat/rate-limit","commit":{"hash":"a1b2c3d4e5f6"},"selector":{"type":"pull-requests","pattern":"**"},"pullrequest":{"id":142}},
 "trigger":{"name":"PUSH","type":"pipeline_trigger_push"},
 "created_on":"2026-10-05T14:22:31.1+00:00","completed_on":"2026-10-05T14:30:02+00:00","duration_in_seconds":451,"build_seconds_used":402}`

const stepsJSON = `[
 {"uuid":"{s1}","name":"Build","image":{"name":"golang:1.25"},"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"started_on":"2026-10-05T14:22:40+00:00","completed_on":"2026-10-05T14:24:42+00:00","duration_in_seconds":122},
 {"uuid":"{s2}","name":"Lint","state":{"name":"COMPLETED","result":{"name":"FAILED"}},"duration_in_seconds":31},
 {"uuid":"{s3}","name":"Deploy","state":{"name":"PENDING"}}]`

func runFixture(t *testing.T, limit int64, logs map[string]stepLog) runInput {
	t.Helper()
	in := runInput{Repo: "api", Limit: limit, Logs: logs}
	if err := json.Unmarshal([]byte(runJSON), &in.Run); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stepsJSON), &in.Steps); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestRunRoot(t *testing.T) {
	logs := map[string]stepLog{"{s1}": {Data: []byte("0123456789"), Total: 40}, "{s2}": {Data: []byte("\x1b[31mred\x1b[0m\r\nok\rdone"), Total: 22}}
	root := runRoot(runFixture(t, 30, logs))
	got := canonical(t, root)
	for _, w := range []string{
		`<pipeline id="api/pipelines/0b1c" number="1873" created="2026-10-05T14:22:31Z" completed="2026-10-05T14:30:02Z" version="completed:failed:2026-10-05T14:30:02Z">`,
		`<state>completed</state>`, `<result>failed</result>`,
		`<trigger type="push" account="712020:aa">Ana Nowak</trigger>`,
		`<target type="pullrequest" ref="feat/rate-limit" commit="a1b2c3d4e5f6" pipeline="pull-requests:**" pr="142"/>`,
		`<duration build-seconds="402">451</duration>`,
		`<step id="s1" n="1" name="Build" image="golang:1.25" state="completed" result="successful" started="2026-10-05T14:22:40Z" completed="2026-10-05T14:24:42Z" duration="122" log-size="40"/>`,
		"<log>red\nok\ndone</log>",
		`<step id="s3" n="3" name="Deploy" state="pending"/>`,
		`<steplog step="s1" name="1-build.log" size="40"/>`,
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("lacks %s:\n%s", w, got)
		}
	}
	if err := validate.Resource(root, root.Clone(), mergedSchema); err != nil {
		t.Fatal(err)
	}
}

func TestLogLimit(t *testing.T) {
	cases := []struct {
		l   stepLog
		big bool
	}{
		{stepLog{Data: []byte("1234"), Total: 4}, false},
		{stepLog{Data: []byte("1234"), Total: 5}, true},
		{stepLog{Data: []byte("1234"), Total: -1}, false},
		{stepLog{Data: []byte("12345"), Total: -1}, true}, // read limit+1 bytes of a running step
	}
	for _, c := range cases {
		if isBig(c.l, 4) != c.big {
			t.Errorf("%+v", c.l)
		}
	}
	root := runRoot(runFixture(t, 0, map[string]stepLog{"{s1}": {Data: nil, Total: 0}}))
	if s := root.ChildrenNamed("step")[0]; attr(s, "log-size") != "0" || s.Child("log") != nil || root.Child("steplog") != nil {
		t.Fatal(canonical(t, root))
	}
}

func TestRunningVersion(t *testing.T) {
	in := runFixture(t, 10, map[string]stepLog{"{s1}": {Data: []byte("a"), Total: -1}})
	in.Run.State = apiState{Name: "IN_PROGRESS"}
	v := runVersion(in)
	in.Logs["{s1}"] = stepLog{Data: []byte("ab"), Total: -1}
	if !strings.HasPrefix(v, "in_progress:") || runVersion(in) == v {
		t.Fatal(v)
	}
	if root := runRoot(in); root.Child("duration") != nil || root.Child("result") == nil && text(root.Child("result")) != "" {
		t.Fatal(canonical(t, root))
	}
}

func TestLogText(t *testing.T) {
	if got := logText([]byte("a\x00b\xffc\x1b]0;title\x07d")); got != "a\uFFFDb\uFFFDcd" {
		t.Fatalf("%q", got)
	}
}
