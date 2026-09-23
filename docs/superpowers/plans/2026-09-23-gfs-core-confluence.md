# gfs core + Confluence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the old Go spike with the adapter-independent `gfs` core from the design spec (XML file model, canonical printer, status/diff/commit/pull/resolve/log/actions, three-way merge, policy, `.gfs/` layout) and one real connector: Confluence Cloud (`confluence://<host>/<SPACE>`).

**Architecture:** Small focused packages under `internal/`: `xmltree` (parse + canonical printing of any XML), `schema` + `canon` (adapter-declared schema and schema-driven normalisation), `envelope` (the `<gfs>` document), `textdiff` (line diff, diff3, unified output), `merge` (structural + textual three-way merge), `workdir` (`.gfs/` config, base, index, log, lock), `adapter` (contract + registry), `adapter/fake` (test remote), `changes` (status + action resolution), `policy`, `engine` (clone/commit/pull/resolve), `cli` (cobra), `adapter/confluence` (REST v2 client, page tree paths, storage-format pass-through). `adapter/fake` is an in-memory test double used by engine tests only; it is never registered in the CLI. Other adapters (mail, jira, slack, dns, x) are **out of scope**.

**Tech Stack:** Go 1.25 (`go.mod` says `go 1.25.0`), standard library `encoding/xml` (RawToken mode), `github.com/spf13/cobra` for the CLI, `golang.org/x/term` for TTY detection. No other third-party dependencies.

**Spec:** `docs/superpowers/specs/2026-09-23-gfs-cli-design.md` (read it before any task). Illustrations of the file format: `example/` (frozen working trees, not test fixtures, not code).

## Global Constraints

- Module path stays `github.com/KrzysztofBogdan/gitfs`; binary name is `gfs`, built from `./cmd/gfs`.
- Go toolchain: `go 1.25.0` in `go.mod`. Run `gofmt -l .` and `go vet ./...` clean before every commit.
- Canonical file: XML declaration `<?xml version="1.0" encoding="UTF-8"?>` on line 1, `<gfs>` on line 2, UTF-8, LF, two-space indentation, trailing newline.
- Any character data containing `<` or `&` is printed as CDATA. No whitespace changes inside mixed content. Printer is idempotent: `canon(canon(x)) == canon(x)`.
- Envelope (`<gfs>`) at rest is bare: no attributes, `<content>` its only child. Reserved envelope children: `<errors>`, `<conflict/>`, `<content>`. Reserved envelope attribute: `action`; every other envelope attribute is a verb parameter.
- Status letters exactly: `A` new, `M` modified, `R` renamed/moved, `D` deleted, `!` last commit failed, `C` conflict.
- Policy values exactly `allow`, `ask`, `deny`. Defaults: every verb `allow` except `send`, `delete`, `publish` = `ask`. On a non-TTY `ask` behaves as `deny`. `--allow <verb>` turns `ask` into `allow` for one run; it never overrides `deny`.
- Exit codes: `0` every action succeeded, `1` any action failed, was denied, or conflicted, `2` usage or configuration error.
- Conflict markers exactly: `<<<<<<< local`, `||||||| base`, `=======`, `>>>>>>> remote v<version>` (base section always present).
- `.gfs/` holds exactly: `config`, `base/`, `index`, `log`, `lock`.
- Log line: `<RFC3339 timestamp> <verb> <path>[ -> <path>] <ok|FAIL|denied> <detail>`.
- Commit report line: `<verb>  <path>[ -> <new path>]   <ok|FAIL|denied|merged>  [<detail>]`, footer `<n> actions, <n> failed, <n> denied`.
- Every command except `clone` finds the working tree by walking up to the nearest `.gfs/`; paths are relative to the current directory.
- Tests use the standard `testing` package only (no testify). Table tests where there is more than one case. No test talks to the network: Confluence is tested against an `httptest.Server` fake.

## Design decisions this plan makes (spec leaves them open)

Read these once; tasks rely on them.

1. **Explicit verb replaces resource-level implicit actions.** If the envelope has `action="send"`, the file resolves to exactly one action `{Verb: "send", Params: ...}` and the adapter receives the full local content. Sub-resource actions are not resolved for that file.
2. **`--allow` and `[policy]` keys are policy classes**, not raw verbs. The class defaults to the verb; the adapter may re-class (X: `create` → `publish`).
3. **Pull cursor** for incremental adapters is stored as the first line of `.gfs/index`: `# cursor <value>`.
4. **Commit conflict updates base to the remote version** (same as pull), so a resolved file commits on the fast path unless the remote moved again.
5. **Sub-resource targets** are `name[id=<id>]` for existing sub-resources and `name[<n>]` (1-based among same-name siblings in the local file) for new ones; nested: `message[id=1.2]/reply[1]`.
6. **Working tree scan** includes every regular file outside `.gfs/`.
7. **Entities**: the parser accepts HTML named entities (`xml.HTMLEntity`) and prints them as literal characters.

## File structure

```
cmd/gfs/main.go                     entry point; calls cli.Execute and os.Exit
internal/xmltree/node.go            Node model and helpers
internal/xmltree/parse.go           Parse (RawToken, tag matching, lenient entities)
internal/xmltree/print.go           canonical formatting of a node tree
internal/schema/schema.go           Schema, Elem, Attr, Kind declarations + lookups
internal/canon/canon.go             schema-driven Normalize (attr order, child order, sub sort, drop empty)
internal/envelope/envelope.go       Doc (action, params, errors, conflict, content), Parse, Bytes, markers
internal/validate/validate.go       schema validation + read-only attribute check
internal/textdiff/lcs.go            line LCS
internal/textdiff/diff3.go          three-way line merge with markers
internal/textdiff/unified.go        unified diff output
internal/merge/merge.go             structural + textual merge of resource roots
internal/workdir/workdir.go         Tree: find root, paths, working file IO, scan
internal/workdir/config.go          INI config parse/write
internal/workdir/index.go           index file (cursor, id, version, path)
internal/workdir/base.go            base snapshot IO
internal/workdir/log.go             append-only log
internal/workdir/lock.go            .gfs/lock
internal/adapter/adapter.go         Adapter/Session interfaces, Resource, Action, Result, Verb, PathModel
internal/adapter/registry.go        scheme -> adapter registry
internal/adapter/fake/fake.go       in-memory test adapter with fault injection (tests only)
internal/adapter/confluence/url.go      confluence://host/SPACE parsing, credentials
internal/adapter/confluence/client.go   REST client: auth, JSON, pagination, error mapping
internal/adapter/confluence/schema.go   <page> schema
internal/adapter/confluence/convert.go  API JSON <-> <page> node, storage body in/out
internal/adapter/confluence/paths.go    page tree -> paths, sanitise, de-duplicate
internal/adapter/confluence/session.go  List/Fetch/Apply/Check
internal/adapter/confluence/cftest/server.go   in-memory httptest Confluence, used by adapter + CLI tests
internal/changes/changes.go         FileChange, Compute (status)
internal/changes/actions.go         Resolve actions from base vs local
internal/policy/policy.go           Decide
internal/engine/engine.go           Env, shared helpers (write-back, base/index update)
internal/engine/clone.go            Clone
internal/engine/commit.go           Commit
internal/engine/pull.go             Pull
internal/engine/resolve.go          Resolve --ours/--theirs
internal/cli/*.go                   cobra commands, one file per command
```

Each package has its `_test.go` next to it.

---

### Task 0: Remove the old Go spike and scaffold `gfs`

The old code (`adapter/`, `adapters/`, `cmd/gitfs/`, `internal/*`) is a Markdown/frontmatter spike that the spec supersedes. Delete it entirely; nothing is ported.

**Files:**
- Delete: `adapter/`, `adapters/`, `cmd/gitfs/`, `internal/` (all tracked Go files listed by `git ls-files '*.go'`)
- Modify: `go.mod`, `go.sum` (via `go mod tidy`), `install.sh`
- Create: `cmd/gfs/main.go`, `internal/cli/root.go`, `internal/cli/root_test.go`, `.gitignore`

**Interfaces:**
- Produces: `cli.Execute() int` (returns process exit code); `cli.NewRoot() *cobra.Command`; `cli.ExitError{Code int; Err error}` for commands to signal exit codes 1/2.

- [ ] **Step 1: Delete the spike**

```bash
git rm -r -q adapter adapters cmd/gitfs internal
git status --short | grep -v '^D ' || true   # only unrelated pre-existing changes should remain
```

- [ ] **Step 2: Write the failing test** `internal/cli/root_test.go`

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsName(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gfs") {
		t.Fatalf("help does not mention gfs:\n%s", out.String())
	}
}

func TestExitCodeFromError(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&ExitError{Code: 1}, 1},
		{&ExitError{Code: 2}, 2},
		{errString("boom"), 2},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/cli/`
Expected: FAIL, `undefined: NewRoot`.

- [ ] **Step 4: Implement** `internal/cli/root.go`

```go
// Package cli wires gfs commands to the engine.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// ExitError carries a non-zero exit code out of a command.
// Code 1: an action failed, was denied or conflicted. Code 2: usage/config error.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 2
}

// NewRoot builds the command tree. Subcommands are added by later tasks.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gfs",
		Short:         "gfs mirrors a net service as a directory of XML files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return root
}

// Execute runs gfs with os.Args and returns the process exit code.
func Execute() int {
	err := NewRoot().Execute()
	if err != nil {
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Err != nil {
			fmt.Fprintln(os.Stderr, "gfs:", err)
		}
	}
	return exitCode(err)
}
```

`cmd/gfs/main.go`:

```go
package main

import (
	"os"

	"github.com/KrzysztofBogdan/gitfs/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
```

`install.sh`: change the build line to `go build -o "$INSTALL_DIR/gfs" ./cmd/gfs`.

`.gitignore`:

```
/gfs
```

- [ ] **Step 5: Tidy and run tests**

Run: `go mod tidy && go test ./... && go vet ./... && gofmt -l .`
Expected: PASS; `go.mod` now requires only `github.com/spf13/cobra` (plus its indirects). `gofmt -l` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add -A cmd internal go.mod go.sum install.sh .gitignore
git commit -m "Remove Go spike, scaffold gfs CLI"
```

---
### Task 1: `xmltree` — node model, parser, canonical formatting

The formatting half of the canonical printer. It knows nothing about schemas: it prints a tree exactly in the order the nodes are in. Schema-driven reordering is Task 2.

Formatting rules (implement exactly):

- An element with **no children** prints as `<name attrs/>`.
- An element whose children are **all text** prints as `<name attrs>TEXT</name>` on one line, text exactly as received (newlines included).
- An element whose text children are **all whitespace** and that has at least one element/comment child is **element-only**: whitespace text is dropped, each non-text child on its own line at depth+1, closing tag on its own line at depth.
- Anything else is **mixed**: children printed inline, verbatim, no whitespace added or removed, recursively (inline elements inside mixed content are printed inline too, whatever their children).
- Text containing `<` or `&` prints as `<![CDATA[...]]>` (with `]]>` split as `]]]]><![CDATA[>`). Other text prints raw.
- Attribute values escape `&`→`&amp;`, `<`→`&lt;`, `>`→`&gt;`, `"`→`&quot;`, `\n`→`&#10;`, `\r`→`&#13;`, `\t`→`&#9;`.
- Names keep their prefix as written (`ac:structured-macro`, `xmlns:ac`). Parsing uses `RawToken`, so no namespace resolution happens and undeclared prefixes are fine.
- A `Raw` node prints its `Text` verbatim, each line as-is, with no indentation (used for conflict markers in Task 6).

**Files:**
- Create: `internal/xmltree/node.go`, `internal/xmltree/parse.go`, `internal/xmltree/print.go`
- Test: `internal/xmltree/xmltree_test.go`

**Interfaces:**
- Produces:
  - `type Kind int` with `Element`, `Text`, `Comment`, `Raw`
  - `type Attr struct{ Name, Value string }`
  - `type Node struct{ Kind Kind; Name string; Attrs []Attr; Children []*Node; Text string }`
  - `func (n *Node) Attr(name string) (string, bool)`, `SetAttr(name, value string)`, `DelAttr(name string)`
  - `func (n *Node) Elements() []*Node` (element children only), `Child(name string) *Node` (first element child by name), `ChildrenNamed(name string) []*Node`
  - `func (n *Node) Clone() *Node` (deep)
  - `func (n *Node) TextContent() string` (concatenated text of direct Text children)
  - `func Parse(r io.Reader) (*Node, error)` — returns the single root element; skips XML declaration, top-level whitespace and comments; errors on zero or several roots, mismatched tags, DOCTYPE
  - `func ParseString(s string) (*Node, error)`
  - `func Print(n *Node, depth int) string` — the element at `depth` (indentation `2*depth` spaces), no trailing newline
  - `func PrintInner(n *Node) string` — children of `n` only, as they would appear between its tags when printed at depth -1 (element-only: children at depth 0 joined by `\n`; mixed/text: inline). Used by adapters to extract a body.
  - `func Equal(a, b *Node) bool` — `Print(a,0) == Print(b,0)`

- [ ] **Step 1: Write the failing tests** `internal/xmltree/xmltree_test.go`

```go
package xmltree

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *Node {
	t.Helper()
	n, err := ParseString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

func TestPrintCases(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty element", `<a  x="1" ></a>`, `<a x="1"/>`},
		{"text only", `<a>hi there</a>`, `<a>hi there</a>`},
		{"text keeps newlines", "<a>line1\n\n  line2</a>", "<a>line1\n\n  line2</a>"},
		{"element only reindented", "<a><b>1</b>\n      <c/></a>", "<a>\n  <b>1</b>\n  <c/>\n</a>"},
		{"nested element only", "<a><b><c>x</c></b></a>", "<a>\n  <b>\n    <c>x</c>\n  </b>\n</a>"},
		{"mixed verbatim", "<p>Fixed in <code>4f2a</code>.\n  ok</p>", "<p>Fixed in <code>4f2a</code>.\n  ok</p>"},
		{"mixed inline children stay inline", "<p>a <b><i>x</i> <i>y</i></b></p>", "<p>a <b><i>x</i> <i>y</i></b></p>"},
		{"cdata for lt", "<a>x &lt; y</a>", "<a><![CDATA[x < y]]></a>"},
		{"cdata for amp", "<a><![CDATA[Q&A]]></a>", "<a><![CDATA[Q&A]]></a>"},
		{"cdata terminator split", "<a>&lt;]]&gt;</a>", "<a><![CDATA[<]]]]><![CDATA[>]]></a>"},
		{"gt stays raw", "<a>x &gt; y</a>", "<a>x > y</a>"},
		{"attr escaping", `<a m="&lt;id@x&gt;" q='say "hi"' n="a&#10;b"/>`, `<a m="&lt;id@x&gt;" q="say &quot;hi&quot;" n="a&#10;b"/>`},
		{"prefixes kept", `<body xmlns:ac="u"><ac:macro ac:name="info"/></body>`, "<body xmlns:ac=\"u\">\n  <ac:macro ac:name=\"info\"/>\n</body>"},
		{"undeclared prefix ok", `<ac:link><ri:page ri:content-title="A"/></ac:link>`, "<ac:link>\n  <ri:page ri:content-title=\"A\"/>\n</ac:link>"},
		{"comment on own line", "<a><!-- c --><b/></a>", "<a>\n  <!-- c -->\n  <b/>\n</a>"},
		{"html entity accepted", "<p>a&nbsp;b</p>", "<p>a b</p>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Print(mustParse(t, c.in), 0)
			if got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestPrintIdempotent(t *testing.T) {
	in := `<page id="1"><title>T</title><body type="application/xhtml+xml" xmlns:ac="u">
<p>Retry budget is <ac:inline-comment-marker ac:ref="c9a1">3 per request</ac:inline-comment-marker>.</p>
<table><tbody><tr><th>S</th><th>O</th></tr></tbody></table>
<ac:plain-text-body><![CDATA[./deploy.sh --tag v2 && echo ok]]></ac:plain-text-body></body></page>`
	once := Print(mustParse(t, in), 0)
	twice := Print(mustParse(t, once), 0)
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

func TestPrintDepthAndRaw(t *testing.T) {
	n := mustParse(t, "<a><b/></a>")
	n.Children = append(n.Children, &Node{Kind: Raw, Text: "<<<<<<< local\n=======\n>>>>>>> remote v2"})
	got := Print(n, 1)
	want := "  <a>\n    <b/>\n<<<<<<< local\n=======\n>>>>>>> remote v2\n  </a>"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestPrintInner(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<body><p>a</p><p>b</p></body>", "<p>a</p>\n<p>b</p>"},
		{"<body>plain &amp; text</body>", "<![CDATA[plain & text]]>"},
		{"<body><ul><li>x</li></ul></body>", "<ul>\n  <li>x</li>\n</ul>"},
	}
	for _, c := range cases {
		if got := PrintInner(mustParse(t, c.in)); got != c.want {
			t.Errorf("PrintInner(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "<a>", "<a></b>", "<a/><b/>", "text", "<!DOCTYPE x><a/>"} {
		if _, err := ParseString(in); err == nil {
			t.Errorf("ParseString(%q): want error", in)
		}
	}
}

func TestParseSkipsDeclAndTopLevelNoise(t *testing.T) {
	n := mustParse(t, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- c -->\n<gfs><content/></gfs>\n")
	if n.Name != "gfs" || n.Child("content") == nil {
		t.Fatalf("got %s", Print(n, 0))
	}
}

func TestHelpers(t *testing.T) {
	n := mustParse(t, `<a x="1"><b>t</b><c/><b>u</b></a>`)
	if v, ok := n.Attr("x"); !ok || v != "1" {
		t.Fatal("Attr")
	}
	n.SetAttr("y", "2")
	n.SetAttr("x", "3")
	n.DelAttr("nope")
	if got := Print(n, 0); !strings.HasPrefix(got, `<a x="3" y="2">`) {
		t.Fatalf("SetAttr order: %s", got)
	}
	if len(n.ChildrenNamed("b")) != 2 || n.Child("b").TextContent() != "t" || len(n.Elements()) != 3 {
		t.Fatal("child helpers")
	}
	c := n.Clone()
	c.Child("b").Children[0].Text = "changed"
	if n.Child("b").TextContent() != "t" {
		t.Fatal("Clone is shallow")
	}
	if !Equal(n, n.Clone()) || Equal(n, c) {
		t.Fatal("Equal")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/xmltree/`
Expected: FAIL, `undefined: ParseString`.

- [ ] **Step 3: Implement** `internal/xmltree/node.go`

```go
// Package xmltree is a small XML tree that round-trips prefixes, keeps mixed
// content verbatim, and prints in gfs canonical formatting.
package xmltree

import "strings"

type Kind int

const (
	Element Kind = iota
	Text
	Comment
	Raw // printed verbatim, no indentation; used for conflict markers
)

type Attr struct{ Name, Value string }

type Node struct {
	Kind     Kind
	Name     string // Element only, with prefix as written
	Attrs    []Attr
	Children []*Node
	Text     string // Text, Comment, Raw
}

func (n *Node) Attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// SetAttr replaces an existing attribute in place or appends a new one.
func (n *Node) SetAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, Attr{name, value})
}

func (n *Node) DelAttr(name string) {
	out := n.Attrs[:0]
	for _, a := range n.Attrs {
		if a.Name != name {
			out = append(out, a)
		}
	}
	n.Attrs = out
}

func (n *Node) Elements() []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == Element {
			out = append(out, c)
		}
	}
	return out
}

func (n *Node) Child(name string) *Node {
	for _, c := range n.Children {
		if c.Kind == Element && c.Name == name {
			return c
		}
	}
	return nil
}

func (n *Node) ChildrenNamed(name string) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == Element && c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *Node) TextContent() string {
	var b strings.Builder
	for _, c := range n.Children {
		if c.Kind == Text {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Attrs = append([]Attr(nil), n.Attrs...)
	c.Children = make([]*Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = ch.Clone()
	}
	return &c
}

func Equal(a, b *Node) bool { return Print(a, 0) == Print(b, 0) }
```

`internal/xmltree/parse.go`:

```go
package xmltree

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// Parse reads one XML document and returns its root element.
func Parse(r io.Reader) (*Node, error) {
	d := xml.NewDecoder(r)
	d.Strict = true
	d.Entity = xml.HTMLEntity
	var root *Node
	var stack []*Node
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Kind: Element, Name: qname(t.Name)}
			for _, a := range t.Attr {
				n.Attrs = append(n.Attrs, Attr{qname(a.Name), a.Value})
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("more than one root element")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].Name != qname(t.Name) {
				return nil, fmt.Errorf("unexpected </%s>", qname(t.Name))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, errors.New("text outside root element")
				}
				continue
			}
			p := stack[len(stack)-1]
			if k := len(p.Children); k > 0 && p.Children[k-1].Kind == Text {
				p.Children[k-1].Text += string(t) // CDATA next to text: one node
			} else {
				p.Children = append(p.Children, &Node{Kind: Text, Text: string(t)})
			}
		case xml.Comment:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, &Node{Kind: Comment, Text: string(t)})
			}
		case xml.ProcInst:
			// XML declaration and processing instructions are dropped.
		case xml.Directive:
			return nil, errors.New("DOCTYPE and other directives are not allowed")
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("unclosed <%s>", stack[len(stack)-1].Name)
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

func ParseString(s string) (*Node, error) { return Parse(strings.NewReader(s)) }
```

`internal/xmltree/print.go`:

```go
package xmltree

import "strings"

type shape int

const (
	empty shape = iota
	textOnly
	elementOnly
	mixed
)

func shapeOf(n *Node) shape {
	if len(n.Children) == 0 {
		return empty
	}
	allText, wsOnly := true, true
	for _, c := range n.Children {
		if c.Kind != Text {
			allText = false
		} else if strings.TrimSpace(c.Text) != "" {
			wsOnly = false
		}
	}
	switch {
	case allText:
		return textOnly
	case wsOnly:
		return elementOnly
	default:
		return mixed
	}
}

var attrEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
	"\n", "&#10;", "\r", "&#13;", "\t", "&#9;")

func startTag(b *strings.Builder, n *Node) {
	b.WriteByte('<')
	b.WriteString(n.Name)
	for _, a := range n.Attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteString(`="`)
		b.WriteString(attrEsc.Replace(a.Value))
		b.WriteByte('"')
	}
}

func writeText(b *strings.Builder, s string) {
	if strings.ContainsAny(s, "<&") {
		b.WriteString("<![CDATA[")
		b.WriteString(strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>"))
		b.WriteString("]]>")
		return
	}
	b.WriteString(s)
}

// Print formats element n at the given depth, without a trailing newline.
func Print(n *Node, depth int) string {
	var b strings.Builder
	writeBlock(&b, n, depth)
	return b.String()
}

func writeBlock(b *strings.Builder, n *Node, depth int) {
	ind := strings.Repeat("  ", depth)
	switch n.Kind {
	case Raw:
		b.WriteString(n.Text)
		return
	case Comment:
		b.WriteString(ind + "<!--" + n.Text + "-->")
		return
	case Text:
		b.WriteString(ind)
		writeText(b, n.Text)
		return
	}
	b.WriteString(ind)
	startTag(b, n)
	switch shapeOf(n) {
	case empty:
		b.WriteString("/>")
	case elementOnly:
		b.WriteString(">\n")
		for _, c := range n.Children {
			if c.Kind == Text {
				continue
			}
			writeBlock(b, c, depth+1)
			b.WriteByte('\n')
		}
		b.WriteString(ind + "</" + n.Name + ">")
	default: // textOnly, mixed
		b.WriteByte('>')
		for _, c := range n.Children {
			writeInline(b, c)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

func writeInline(b *strings.Builder, n *Node) {
	switch n.Kind {
	case Text:
		writeText(b, n.Text)
	case Comment:
		b.WriteString("<!--" + n.Text + "-->")
	case Raw:
		b.WriteString(n.Text)
	case Element:
		startTag(b, n)
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteByte('>')
		for _, c := range n.Children {
			writeInline(b, c)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

// PrintInner formats only the children of n, as if n were printed at depth -1.
func PrintInner(n *Node) string {
	var b strings.Builder
	switch shapeOf(n) {
	case empty:
	case elementOnly:
		first := true
		for _, c := range n.Children {
			if c.Kind == Text {
				continue
			}
			if !first {
				b.WriteByte('\n')
			}
			first = false
			writeBlock(&b, c, 0)
		}
	default:
		for _, c := range n.Children {
			writeInline(&b, c)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/xmltree/ -v`
Expected: PASS, all cases.

- [ ] **Step 5: Commit**

```bash
git add internal/xmltree
git commit -m "xmltree: parser and canonical formatting"
```

---
### Task 2: `schema` declarations and `canon.Normalize`

The schema-driven half of the canonical printer: attribute order, child order, sub-resource sort, dropping empty optional fields. After `Normalize`, `xmltree.Print` produces canonical text.

Rules:

- Root attributes: `RootAttrs` order first, then any others sorted by name.
- Root children: grouped in `Elems` order (stable within a group); unknown elements keep their relative order after all known ones (validation rejects them later); comments are dropped; whitespace text is dropped.
- `Field` / `List` elements: attributes in declared order then sorted; dropped entirely if they have no attributes, no element children and whitespace-only text. `List` with `Sorted: true` sorts its items by text content.
- `Body` elements: attributes in declared order then sorted; every descendant element's attributes sorted by name; text and structure untouched.
- `Sub` elements: attributes in declared order then sorted; sorted by `SortKey` attribute (string compare), elements lacking the key after all keyed ones in their original order; declared `Children` of a sub are normalised recursively with the same rules as root children; undeclared content of a sub is treated like a body (attributes sorted, nothing else).

**Files:**
- Create: `internal/schema/schema.go`, `internal/canon/canon.go`
- Test: `internal/canon/canon_test.go`

**Interfaces:**
- Consumes: `xmltree.Node`, `xmltree.Print`, `xmltree.ParseString` (Task 1).
- Produces (package `schema`):
  - `type Kind int` with `Field`, `List`, `Body`, `Sub`
  - `type Attr struct{ Name string; ReadOnly bool }`
  - `type Elem struct{ Name string; Kind Kind; Repeated bool; Item string; Sorted bool; Attrs []Attr; ID string; SortKey string; Children []Elem; BodyTypes []string }`
  - `type Schema struct{ Root string; RootAttrs []Attr; ID string; Version string; Elems []Elem }` (`ID`: identity attribute of the root; `Version`: version attribute of the root, may be empty)
  - `func Find(elems []Elem, name string) *Elem`
  - `func (a Attr) ...` none; `func AttrDecl(attrs []Attr, name string) (Attr, bool)`
- Produces (package `canon`): `func Normalize(root *xmltree.Node, s *schema.Schema)` (mutates in place; `root` is the resource root, e.g. `<page>`).

- [ ] **Step 1: Write the failing test** `internal/canon/canon_test.go`

```go
package canon

import (
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var testSchema = &schema.Schema{
	Root:      "page",
	ID:        "id",
	Version:   "version",
	RootAttrs: []schema.Attr{{"id", true}, {"version", true}, {"parent", true}, {"created", true}, {"updated", true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{"type", false}}, BodyTypes: []string{"application/xhtml+xml"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{"id", true}, {"author", true}, {"created", true}}},
	},
}

func canonical(t *testing.T, in string) string {
	t.Helper()
	n, err := xmltree.ParseString(in)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(n, testSchema)
	return xmltree.Print(n, 0)
}

func TestNormalize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"root attr order", `<page updated="u" zz="1" id="1" aa="2"><title>T</title></page>`,
			"<page id=\"1\" updated=\"u\" aa=\"2\" zz=\"1\">\n  <title>T</title>\n</page>"},
		{"child order by schema", `<page><comment id="9" created="2">c</comment><body type="text/plain">b</body><title>T</title></page>`,
			"<page>\n  <title>T</title>\n  <body type=\"text/plain\">b</body>\n  <comment id=\"9\" created=\"2\">c</comment>\n</page>"},
		{"subs sorted, new last", `<page><comment>new</comment><comment id="2" created="2026-02">b</comment><comment id="1" created="2026-01">a</comment></page>`,
			"<page>\n  <comment id=\"1\" created=\"2026-01\">a</comment>\n  <comment id=\"2\" created=\"2026-02\">b</comment>\n  <comment>new</comment>\n</page>"},
		{"empty fields dropped", `<page><title> </title><labels/></page>`, `<page/>`},
		{"list sorted", `<page><labels><label>b</label><label>a</label></labels></page>`,
			"<page>\n  <labels>\n    <label>a</label>\n    <label>b</label>\n  </labels>\n</page>"},
		{"body attrs sorted inside, text untouched", `<page><body xmlns:ac="u" type="application/xhtml+xml"><p>x <ac:m ac:z="1" ac:a="2">y</ac:m></p></body></page>`,
			"<page>\n  <body type=\"application/xhtml+xml\" xmlns:ac=\"u\">\n    <p>x <ac:m ac:a=\"2\" ac:z=\"1\">y</ac:m></p>\n  </body>\n</page>"},
		{"comments and unknown kept order", `<page><!-- x --><zzz/><title>T</title><yyy/></page>`,
			"<page>\n  <title>T</title>\n  <zzz/>\n  <yyy/>\n</page>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonical(t, c.in); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	in := `<page version="3" id="1"><labels><label>z</label><label>a</label></labels><comment>n</comment><title>T</title><body type="application/xhtml+xml"><p>a</p><ul><li b="1" a="2">x</li></ul></body></page>`
	once := canonical(t, in)
	if twice := canonical(t, once); once != twice {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/canon/`
Expected: FAIL, package `schema` / `Normalize` undefined.

- [ ] **Step 3: Implement** `internal/schema/schema.go`

```go
// Package schema declares the shape of an adapter's resource root.
package schema

type Kind int

const (
	Field Kind = iota // scalar text element (Repeated: may occur several times)
	List              // container of repeated Item elements
	Body              // native service content, passed through
	Sub               // sub-resource with identity attribute ID
)

type Attr struct {
	Name     string
	ReadOnly bool // written by gfs from the service; a local change is a commit error
}

type Elem struct {
	Name      string
	Kind      Kind
	Repeated  bool     // Field only
	Item      string   // List only: item element name
	Sorted    bool     // List only: items are unordered, sort by text
	Attrs     []Attr   // declared attributes in canonical order
	ID        string   // Sub only: identity attribute
	SortKey   string   // Sub only: attribute to sort by
	Children  []Elem   // Sub only: declared nested elements (e.g. reply)
	BodyTypes []string // Body only: allowed values of the type attribute
}

type Schema struct {
	Root      string
	RootAttrs []Attr
	ID        string // identity attribute of the root
	Version   string // version attribute of the root, "" if none
	Elems     []Elem // root children in canonical order
}

func Find(elems []Elem, name string) *Elem {
	for i := range elems {
		if elems[i].Name == name {
			return &elems[i]
		}
	}
	return nil
}

func AttrDecl(attrs []Attr, name string) (Attr, bool) {
	for _, a := range attrs {
		if a.Name == name {
			return a, true
		}
	}
	return Attr{}, false
}
```

`internal/canon/canon.go`:

```go
// Package canon applies an adapter schema to a resource tree so that
// xmltree.Print yields the canonical form.
package canon

import (
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Normalize reorders and prunes root in place according to s.
func Normalize(root *xmltree.Node, s *schema.Schema) {
	orderAttrs(root, s.RootAttrs)
	normalizeChildren(root, s.Elems)
}

func orderAttrs(n *xmltree.Node, decl []schema.Attr) {
	var head, tail []xmltree.Attr
	for _, d := range decl {
		if v, ok := n.Attr(d.Name); ok {
			head = append(head, xmltree.Attr{Name: d.Name, Value: v})
		}
	}
	for _, a := range n.Attrs {
		if _, ok := schema.AttrDecl(decl, a.Name); !ok {
			tail = append(tail, a)
		}
	}
	sort.SliceStable(tail, func(i, j int) bool { return tail[i].Name < tail[j].Name })
	n.Attrs = append(head, tail...)
}

func sortAttrsDeep(n *xmltree.Node) {
	for _, c := range n.Children {
		if c.Kind == xmltree.Element {
			orderAttrs(c, nil)
			sortAttrsDeep(c)
		}
	}
}

func isEmpty(n *xmltree.Node) bool {
	return len(n.Attrs) == 0 && len(n.Elements()) == 0 && strings.TrimSpace(n.TextContent()) == ""
}

func normalizeChildren(parent *xmltree.Node, elems []schema.Elem) {
	groups := make([][]*xmltree.Node, len(elems))
	var unknown []*xmltree.Node
	for _, c := range parent.Children {
		if c.Kind != xmltree.Element {
			continue // whitespace text and comments are dropped
		}
		i := indexOf(elems, c.Name)
		if i < 0 {
			unknown = append(unknown, c)
			continue
		}
		e := &elems[i]
		orderAttrs(c, e.Attrs)
		switch e.Kind {
		case schema.Field, schema.List:
			if isEmpty(c) {
				continue
			}
			if e.Kind == schema.List && e.Sorted {
				items := c.Elements()
				sort.SliceStable(items, func(a, b int) bool { return items[a].TextContent() < items[b].TextContent() })
				c.Children = items
			}
		case schema.Body:
			sortAttrsDeep(c)
		case schema.Sub:
			normalizeSub(c, e)
		}
		groups[i] = append(groups[i], c)
	}
	var out []*xmltree.Node
	for i, g := range groups {
		if elems[i].Kind == schema.Sub && elems[i].SortKey != "" {
			key := elems[i].SortKey
			sort.SliceStable(g, func(a, b int) bool {
				ka, oka := g[a].Attr(key)
				kb, okb := g[b].Attr(key)
				if oka != okb {
					return oka // keyed before unkeyed
				}
				return oka && ka < kb
			})
		}
		out = append(out, g...)
	}
	parent.Children = append(out, unknown...)
}

// normalizeSub normalises declared nested children and treats the rest like a body.
func normalizeSub(n *xmltree.Node, e *schema.Elem) {
	if len(e.Children) == 0 {
		sortAttrsDeep(n)
		return
	}
	var own, nested []*xmltree.Node
	for _, c := range n.Children {
		if c.Kind == xmltree.Element && indexOf(e.Children, c.Name) >= 0 {
			nested = append(nested, c)
		} else {
			own = append(own, c)
		}
	}
	tmp := &xmltree.Node{Kind: xmltree.Element, Children: nested}
	normalizeChildren(tmp, e.Children)
	for _, c := range own {
		if c.Kind == xmltree.Element {
			orderAttrs(c, nil)
			sortAttrsDeep(c)
		}
	}
	n.Children = append(own, tmp.Children...)
}

func indexOf(elems []schema.Elem, name string) int {
	for i := range elems {
		if elems[i].Name == name {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/canon/ ./internal/xmltree/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/schema internal/canon
git commit -m "schema and canon: schema-driven canonical normalisation"
```

---
### Task 3: `envelope` — the `<gfs>` document

Parses and prints a whole file. Knows the reserved envelope parts; treats the resource root as opaque (schema is passed in only to normalise it).

Rules:

- `Parse` accepts a full document (`<gfs>` root) or, leniently, a bare resource root (any other root name), which it wraps. It rejects: a `<gfs>` with no `<content>`, `<content>` with anything other than exactly one element child, unknown envelope children.
- `HasMarkers(data)` is true if any line starts with `<<<<<<< `, `=======` (the whole line), `||||||| ` or `>>>>>>> `. `HasConflictElement(data)` is true if the bytes contain `<conflict ` or `<conflict/>` before `<content>`. Both are checked on raw bytes because a conflicted file is not well-formed.
- `Bytes(d, s)` normalises `d.Content` with `canon.Normalize(d.Content, s)` (when `s != nil`) and prints: declaration line, `<gfs ...>` with `action` first then params sorted by name, then `<errors>` (if any), `<conflict/>` (if any), `<content>`, trailing newline.
- `<error>` attributes in order `action target code at`; `target` and `code` omitted when empty; `<msg>` text child.
- `<conflict>` attributes in order `remote-version by at elements hunks`.
- `Bare()` returns a copy with action, params, errors and conflict removed.

**Files:**
- Create: `internal/envelope/envelope.go`
- Test: `internal/envelope/envelope_test.go`

**Interfaces:**
- Consumes: `xmltree` (Task 1), `schema`, `canon.Normalize` (Task 2).
- Produces:
  - `type Error struct{ Action, Target, Code, At, Msg string }`
  - `type Conflict struct{ RemoteVersion, By, At string; Elements, Hunks int }`
  - `type Doc struct{ Action string; Params map[string]string; Errors []Error; Conflict *Conflict; Content *xmltree.Node; Wrapped bool }` (`Wrapped`: input was a bare root)
  - `func Parse(data []byte) (*Doc, error)`
  - `func New(root *xmltree.Node) *Doc`
  - `func Bytes(d *Doc, s *schema.Schema) []byte`
  - `func (d *Doc) Bare() *Doc`
  - `func HasMarkers(data []byte) bool`, `func HasConflictElement(data []byte) bool`
  - `func Header(d *Doc) string` — the canonical text up to and including the `<content>` line; `const Footer = "  </content>\n</gfs>\n"`. Used by merge and commit (Tasks 6, 12) to splice marker lines into content.

- [ ] **Step 1: Write the failing test** `internal/envelope/envelope_test.go`

```go
package envelope

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const bare = `<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <page id="1">
      <title>T</title>
    </page>
  </content>
</gfs>
`

func TestRoundTripBare(t *testing.T) {
	d, err := Parse([]byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if d.Wrapped || d.Action != "" || d.Content.Name != "page" {
		t.Fatalf("%+v", d)
	}
	if got := string(Bytes(d, nil)); got != bare {
		t.Fatalf("got\n%s", got)
	}
}

func TestLenientBareRoot(t *testing.T) {
	d, err := Parse([]byte("<page>\n<title>T</title></page>"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Wrapped {
		t.Fatal("want Wrapped")
	}
	d.Content.SetAttr("id", "1")
	if got := string(Bytes(d, nil)); got != bare {
		t.Fatalf("got\n%s", got)
	}
}

func TestActionParamsErrorsConflict(t *testing.T) {
	in := `<gfs to="x" action="send" cc="y"><errors><error code="550" action="send" at="t1"><msg>550 &lt;bob&gt; rejected</msg></error></errors><content><mail/></content></gfs>`
	d, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "send" || d.Params["to"] != "x" || d.Params["cc"] != "y" || len(d.Errors) != 1 || d.Errors[0].Msg != "550 <bob> rejected" {
		t.Fatalf("%+v", d)
	}
	d.Conflict = &Conflict{RemoteVersion: "8", By: "bob", At: "t2", Elements: 1, Hunks: 2}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<gfs action="send" cc="y" to="x">
  <errors>
    <error action="send" code="550" at="t1">
      <msg><![CDATA[550 <bob> rejected]]></msg>
    </error>
  </errors>
  <conflict remote-version="8" by="bob" at="t2" elements="1" hunks="2"/>
  <content>
    <mail/>
  </content>
</gfs>
`
	if got := string(Bytes(d, nil)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	b := d.Bare()
	if b.Action != "" || len(b.Params) != 0 || b.Errors != nil || b.Conflict != nil || d.Action != "send" {
		t.Fatal("Bare must copy and strip")
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{
		`<gfs/>`,
		`<gfs><content/></gfs>`,
		`<gfs><content><a/><b/></content></gfs>`,
		`<gfs><content><a/></content><extra/></gfs>`,
		`not xml`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q): want error", in)
		}
	}
}

func TestMarkers(t *testing.T) {
	conflicted := "<gfs>\n  <conflict remote-version=\"8\"/>\n  <content>\n<<<<<<< local\na\n||||||| base\n=======\nb\n>>>>>>> remote v8\n"
	if !HasMarkers([]byte(conflicted)) || !HasConflictElement([]byte(conflicted)) {
		t.Fatal("want markers and conflict element")
	}
	if HasMarkers([]byte(bare)) || HasConflictElement([]byte(bare)) {
		t.Fatal("bare has none")
	}
	if HasConflictElement([]byte("<gfs>\n  <content>\n    <x><conflict/></x>")) {
		t.Fatal("conflict inside content is not the envelope element")
	}
}

func TestHeaderFooter(t *testing.T) {
	d := New(&xmltree.Node{Kind: xmltree.Element, Name: "page"})
	full := string(Bytes(d, nil))
	if !strings.HasPrefix(full, Header(d)) || !strings.HasSuffix(full, Footer) {
		t.Fatalf("header/footer do not frame %q", full)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/envelope/`
Expected: FAIL, undefined `Parse`.

- [ ] **Step 3: Implement** `internal/envelope/envelope.go`

```go
// Package envelope reads and writes the <gfs> document around a resource.
package envelope

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const Decl = `<?xml version="1.0" encoding="UTF-8"?>`

// Footer closes every canonical document.
const Footer = "  </content>\n</gfs>\n"

type Error struct{ Action, Target, Code, At, Msg string }

type Conflict struct {
	RemoteVersion, By, At string
	Elements, Hunks       int
}

type Doc struct {
	Action   string
	Params   map[string]string
	Errors   []Error
	Conflict *Conflict
	Content  *xmltree.Node // the resource root
	Wrapped  bool          // input was a bare resource root
}

func New(root *xmltree.Node) *Doc { return &Doc{Content: root, Params: map[string]string{}} }

func Parse(data []byte) (*Doc, error) {
	root, err := xmltree.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if root.Name != "gfs" {
		d := New(root)
		d.Wrapped = true
		return d, nil
	}
	d := New(nil)
	for _, a := range root.Attrs {
		if a.Name == "action" {
			d.Action = a.Value
		} else {
			d.Params[a.Name] = a.Value
		}
	}
	for _, c := range root.Elements() {
		switch c.Name {
		case "content":
			if d.Content != nil {
				return nil, errors.New("more than one <content>")
			}
			els := c.Elements()
			if len(els) != 1 || strings.TrimSpace(c.TextContent()) != "" {
				return nil, errors.New("<content> must hold exactly one resource element")
			}
			d.Content = els[0]
		case "errors":
			for _, e := range c.ChildrenNamed("error") {
				er := Error{Msg: textOf(e.Child("msg"))}
				er.Action, _ = e.Attr("action")
				er.Target, _ = e.Attr("target")
				er.Code, _ = e.Attr("code")
				er.At, _ = e.Attr("at")
				d.Errors = append(d.Errors, er)
			}
		case "conflict":
			cf := &Conflict{}
			cf.RemoteVersion, _ = c.Attr("remote-version")
			cf.By, _ = c.Attr("by")
			cf.At, _ = c.Attr("at")
			v, _ := c.Attr("elements")
			cf.Elements, _ = strconv.Atoi(v)
			v, _ = c.Attr("hunks")
			cf.Hunks, _ = strconv.Atoi(v)
			d.Conflict = cf
		default:
			return nil, fmt.Errorf("unknown envelope element <%s>", c.Name)
		}
	}
	if d.Content == nil {
		return nil, errors.New("missing <content>")
	}
	return d, nil
}

func textOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	return n.TextContent()
}

func (d *Doc) Bare() *Doc {
	return &Doc{Content: d.Content, Params: map[string]string{}, Wrapped: d.Wrapped}
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.Attrs = append(n.Attrs, xmltree.Attr{Name: attrs[i], Value: attrs[i+1]})
		}
	}
	return n
}

func envelopeNode(d *Doc) *xmltree.Node {
	g := el("gfs", "action", d.Action)
	keys := make([]string, 0, len(d.Params))
	for k := range d.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g.Attrs = append(g.Attrs, xmltree.Attr{Name: k, Value: d.Params[k]})
	}
	if len(d.Errors) > 0 {
		es := el("errors")
		for _, e := range d.Errors {
			en := el("error", "action", e.Action, "target", e.Target, "code", e.Code, "at", e.At)
			m := el("msg")
			m.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: e.Msg}}
			en.Children = []*xmltree.Node{m}
			es.Children = append(es.Children, en)
		}
		g.Children = append(g.Children, es)
	}
	if c := d.Conflict; c != nil {
		g.Children = append(g.Children, el("conflict", "remote-version", c.RemoteVersion, "by", c.By, "at", c.At,
			"elements", strconv.Itoa(c.Elements), "hunks", strconv.Itoa(c.Hunks)))
	}
	return g
}

// Header returns the canonical text up to and including the "<content>" line.
func Header(d *Doc) string {
	g := envelopeNode(d)
	g.Children = append(g.Children, el("content"))
	s := xmltree.Print(g, 0) // "...\n  <content/>\n</gfs>"
	s = strings.TrimSuffix(s, "  <content/>\n</gfs>")
	return Decl + "\n" + s + "  <content>\n"
}

// Bytes renders d in canonical form. s may be nil (no schema normalisation).
func Bytes(d *Doc, s *schema.Schema) []byte {
	if s != nil {
		canon.Normalize(d.Content, s)
	}
	return []byte(Header(d) + xmltree.Print(d.Content, 2) + "\n" + Footer)
}

func HasMarkers(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if line == "=======" || strings.HasPrefix(line, "<<<<<<< ") ||
			strings.HasPrefix(line, "||||||| ") || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
}

func HasConflictElement(data []byte) bool {
	s := string(data)
	if i := strings.Index(s, "<content>"); i >= 0 {
		s = s[:i]
	}
	return strings.Contains(s, "<conflict ") || strings.Contains(s, "<conflict/>")
}
```

Note on `Header`: `xmltree.Print` of `<gfs>` with only an empty `<content/>` child ends in `"\n  <content/>\n</gfs>"`; with no attributes the result is `"<gfs>\n  <content/>\n</gfs>"`, so trimming leaves `"<gfs>\n"` and the header is `Decl\n<gfs>\n  <content>\n`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/envelope/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/envelope
git commit -m "envelope: parse and print the <gfs> document"
```

---
### Task 4: `validate` — schema and read-only attribute checks

Step 1 of the commit algorithm ("adapter schema, read-only attributes unchanged") and step 3 of merge ("merged document must pass the adapter schema").

Rules (each violation is one error; all are collected with `errors.Join`):

- Root element name must be `s.Root`.
- Every root child element must be declared in `s.Elems`. Text children must be whitespace.
- A non-`Repeated` `Field`, a `List`, a `Body` may occur at most once.
- A `List` may contain only `Item` elements.
- A `Body` must have a `type` attribute whose value is in `BodyTypes`.
- Read-only attributes of the root must equal those of `ref` (the base or remote root; `ref == nil` means "new resource": read-only attributes must be absent).
- A `Sub` with its `ID` attribute must match a sub of the same name and ID in `ref`, and its read-only attributes must equal that one's. A `Sub` without `ID` is new: its read-only attributes must be absent. Declared `Children` of subs are checked recursively with the same rules.
- Error texts name the element path, e.g. `comment[id=7731]: read-only attribute "author" changed`, `page: read-only attribute "version" changed`, `unknown element <foo>`.

**Files:**
- Create: `internal/validate/validate.go`
- Test: `internal/validate/validate_test.go`

**Interfaces:**
- Consumes: `schema` (Task 2), `xmltree` (Task 1).
- Produces: `func Resource(root, ref *xmltree.Node, s *schema.Schema) error`

- [ ] **Step 1: Write the failing test** `internal/validate/validate_test.go`

```go
package validate

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var s = &schema.Schema{
	Root: "page", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{"id", true}, {"version", true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label"},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{"type", false}}, BodyTypes: []string{"application/xhtml+xml"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", Attrs: []schema.Attr{{"id", true}, {"author", true}}},
	},
}

func p(t *testing.T, x string) *xmltree.Node {
	t.Helper()
	n, err := xmltree.ParseString(x)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

const base = `<page id="1" version="3"><title>T</title><comment id="7" author="bob">c</comment></page>`

func TestResource(t *testing.T) {
	cases := []struct{ name, local, ref, wantErr string }{
		{"ok unchanged", base, base, ""},
		{"ok new comment", `<page id="1" version="3"><title>T</title><comment id="7" author="bob">c</comment><comment>n</comment></page>`, base, ""},
		{"ok new page", `<page><title>T</title><body type="application/xhtml+xml"><p/></body></page>`, "", ""},
		{"wrong root", `<issue/>`, "", "root element <issue>, want <page>"},
		{"unknown element", `<page><foo/></page>`, "", "unknown element <foo>"},
		{"stray text", `<page>hello<title>T</title></page>`, "", "text outside elements"},
		{"field twice", `<page><title>a</title><title>b</title></page>`, "", "<title> occurs more than once"},
		{"bad list item", `<page><labels><tag>x</tag></labels></page>`, "", "<labels> may only contain <label>"},
		{"body no type", `<page><body><p/></body></page>`, "", `<body> type "" not allowed`},
		{"body wrong type", `<page><body type="text/html">x</body></page>`, "", `<body> type "text/html" not allowed`},
		{"version edited", `<page id="1" version="4"><title>T</title><comment id="7" author="bob">c</comment></page>`, base, `page: read-only attribute "version" changed`},
		{"id on new page", `<page id="5"/>`, "", `page: read-only attribute "id" changed`},
		{"comment author edited", `<page id="1" version="3"><comment id="7" author="eve">c</comment></page>`, base, `comment[id=7]: read-only attribute "author" changed`},
		{"unknown comment id", `<page id="1" version="3"><comment id="99">c</comment></page>`, base, `comment[id=99]: no such comment on the remote`},
		{"new comment with author", `<page id="1" version="3"><comment author="me">c</comment></page>`, base, `comment[1]: read-only attribute "author" changed`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ref *xmltree.Node
			if c.ref != "" {
				ref = p(t, c.ref)
			}
			err := Resource(p(t, c.local), ref, s)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want %q", err, c.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/validate/`
Expected: FAIL, undefined `Resource`.

- [ ] **Step 3: Implement** `internal/validate/validate.go`

```go
// Package validate checks a resource tree against its adapter schema.
package validate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// Resource validates root against s. ref is the remote/base version of the
// same resource, or nil for a new one.
func Resource(root, ref *xmltree.Node, s *schema.Schema) error {
	var errs []error
	if root.Name != s.Root {
		return fmt.Errorf("root element <%s>, want <%s>", root.Name, s.Root)
	}
	errs = append(errs, readOnly(s.Root, root, ref, s.RootAttrs)...)
	errs = append(errs, children(root, ref, s.Elems)...)
	return errors.Join(errs...)
}

func readOnly(label string, n, ref *xmltree.Node, decl []schema.Attr) []error {
	var errs []error
	for _, d := range decl {
		if !d.ReadOnly {
			continue
		}
		v, ok := n.Attr(d.Name)
		var rv string
		var rok bool
		if ref != nil {
			rv, rok = ref.Attr(d.Name)
		}
		if ok != rok || v != rv {
			errs = append(errs, fmt.Errorf("%s: read-only attribute %q changed", label, d.Name))
		}
	}
	return errs
}

func children(n, ref *xmltree.Node, elems []schema.Elem) []error {
	var errs []error
	count := map[string]int{}
	newIdx := map[string]int{}
	for _, c := range n.Children {
		switch c.Kind {
		case xmltree.Text:
			if strings.TrimSpace(c.Text) != "" {
				errs = append(errs, fmt.Errorf("<%s>: text outside elements", n.Name))
			}
			continue
		case xmltree.Element:
		default:
			continue
		}
		e := schema.Find(elems, c.Name)
		if e == nil {
			errs = append(errs, fmt.Errorf("unknown element <%s>", c.Name))
			continue
		}
		count[c.Name]++
		switch e.Kind {
		case schema.Field, schema.List, schema.Body:
			if count[c.Name] == 2 && !(e.Kind == schema.Field && e.Repeated) {
				errs = append(errs, fmt.Errorf("<%s> occurs more than once", c.Name))
			}
		}
		switch e.Kind {
		case schema.List:
			for _, it := range c.Elements() {
				if it.Name != e.Item {
					errs = append(errs, fmt.Errorf("<%s> may only contain <%s>", c.Name, e.Item))
					break
				}
			}
		case schema.Body:
			typ, _ := c.Attr("type")
			if !slices.Contains(e.BodyTypes, typ) {
				errs = append(errs, fmt.Errorf("<%s> type %q not allowed", c.Name, typ))
			}
		case schema.Sub:
			errs = append(errs, sub(c, ref, e, newIdx)...)
		}
	}
	return errs
}

func sub(c, parentRef *xmltree.Node, e *schema.Elem, newIdx map[string]int) []error {
	id, hasID := c.Attr(e.ID)
	if !hasID {
		newIdx[c.Name]++
		label := fmt.Sprintf("%s[%d]", c.Name, newIdx[c.Name])
		return append(readOnly(label, c, nil, withoutID(e)), children(ownless(c, e), nil, e.Children)...)
	}
	label := fmt.Sprintf("%s[id=%s]", c.Name, id)
	ref := FindSub(parentRef, c.Name, e.ID, id)
	if ref == nil {
		return []error{fmt.Errorf("%s: no such %s on the remote", label, c.Name)}
	}
	return append(readOnly(label, c, ref, e.Attrs), children(ownless(c, e), ref, e.Children)...)
}

// withoutID drops the identity attribute: its absence is what makes a sub new.
func withoutID(e *schema.Elem) []schema.Attr {
	var out []schema.Attr
	for _, a := range e.Attrs {
		if a.Name != e.ID {
			out = append(out, a)
		}
	}
	return out
}

// ownless returns a view of c holding only its declared nested children, so
// that a sub's own (body-like) content is not checked as schema elements.
func ownless(c *xmltree.Node, e *schema.Elem) *xmltree.Node {
	v := &xmltree.Node{Kind: xmltree.Element, Name: c.Name}
	for _, ch := range c.Children {
		if ch.Kind == xmltree.Element && schema.Find(e.Children, ch.Name) != nil {
			v.Children = append(v.Children, ch)
		}
	}
	return v
}

// FindSub returns parent's child element named name whose attribute idAttr is id.
func FindSub(parent *xmltree.Node, name, idAttr, id string) *xmltree.Node {
	if parent == nil {
		return nil
	}
	for _, c := range parent.ChildrenNamed(name) {
		if v, ok := c.Attr(idAttr); ok && v == id {
			return c
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/validate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/validate
git commit -m "validate: schema and read-only attribute checks"
```

---
### Task 5: `textdiff` — LCS, diff3, unified diff

Line-based text algorithms shared by merge (Task 6) and `gfs diff` (Task 15). Inputs are canonical text split on `\n`.

Semantics (git-compatible where it matters):

- `Merge3` splits into regions separated by lines that are unchanged on both sides. For each unstable region: local == base → take remote; remote == base → take local; local == remote → take it; otherwise a conflict. As in git, changes on **adjacent** lines conflict; there must be at least one untouched line between two changes for them to merge.
- `Render` writes conflicts with the exact markers from Global Constraints; the base section is always present, possibly empty.
- `Unified` prints `--- <a>` / `+++ <b>` headers and `@@ -s,n +s,n @@` hunks with 3 lines of context, git-style range formatting (`,n` omitted when `n == 1`; start is `s+1`, or `s` when `n == 0`). Equal inputs produce the empty string.
- The LCS is a dynamic-programming table over the middle after trimming common prefix and suffix, using `int32` cells. This is O(n·m) and fine for pages of a few thousand lines.

**Files:**
- Create: `internal/textdiff/lcs.go`, `internal/textdiff/diff3.go`, `internal/textdiff/unified.go`
- Test: `internal/textdiff/textdiff_test.go`

**Interfaces:**
- Produces:
  - `func Lines(s string) []string` — `strings.Split(s, "\n")` without a trailing empty element when `s` ends in `\n`
  - `func Matches(a, b []string) []int` — `m[i]` is the index in `b` matched to `a[i]`, or `-1`; matches are strictly increasing
  - `type Chunk struct{ Conflict bool; Lines []string; Local, Base, Remote []string }` (`Lines` for clean chunks)
  - `func Merge3(base, local, remote []string) []Chunk`
  - `func Render(chunks []Chunk, remoteLabel string) (lines []string, conflicts int)` — `remoteLabel` like `"remote v8"`
  - `func Unified(aName, bName string, a, b []string) string`

- [ ] **Step 1: Write the failing test** `internal/textdiff/textdiff_test.go`

```go
package textdiff

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func L(s string) []string { return Lines(s) }

func TestLines(t *testing.T) {
	if got := Lines("a\nb\n"); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatal(got)
	}
	if got := Lines(""); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestMatches(t *testing.T) {
	m := Matches(L("a\nb\nc\nd"), L("a\nx\nc\nd\ne"))
	if !slices.Equal(m, []int{0, -1, 2, 3}) {
		t.Fatal(m)
	}
}

func TestMerge3(t *testing.T) {
	cases := []struct {
		name, base, local, remote string
		want                      string
		conflicts                 int
	}{
		{"local only", "a\nb\nc", "a\nB\nc", "a\nb\nc", "a\nB\nc", 0},
		{"remote only", "a\nb\nc", "a\nb\nc", "a\nb\nC", "a\nb\nC", 0},
		{"same change", "a\nb\nc", "a\nX\nc", "a\nX\nc", "a\nX\nc", 0},
		{"separate changes", "a\nb\nc\nd\ne", "a\nB\nc\nd\ne", "a\nb\nc\nd\nE", "a\nB\nc\nd\nE", 0},
		{"local insert remote delete elsewhere", "a\nb\nc\nd", "a\nnew\nb\nc\nd", "a\nb\nc", "a\nnew\nb\nc", 0},
		{"overlap", "a\nb\nc", "a\nL\nc", "a\nR\nc",
			"a\n<<<<<<< local\nL\n||||||| base\nb\n=======\nR\n>>>>>>> remote v8\nc", 1},
		{"both insert same spot", "a\nc", "a\nL\nc", "a\nR\nc",
			"a\n<<<<<<< local\nL\n||||||| base\n=======\nR\n>>>>>>> remote v8\nc", 1},
		{"adjacent changes conflict", "a\nb\nc", "a\nB\nc", "a\nb\nC",
			"a\n<<<<<<< local\nB\nc\n||||||| base\nb\nc\n=======\nb\nC\n>>>>>>> remote v8", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := Render(Merge3(L(c.base), L(c.local), L(c.remote)), "remote v8")
			if strings.Join(got, "\n") != c.want || n != c.conflicts {
				t.Fatalf("got (%d conflicts)\n%s\nwant (%d)\n%s", n, strings.Join(got, "\n"), c.conflicts, c.want)
			}
		})
	}
}

func TestUnified(t *testing.T) {
	var a, b []string
	for i := 1; i <= 20; i++ {
		a = append(a, fmt.Sprint(i))
		b = append(b, fmt.Sprint(i))
	}
	b[4] = "five"
	b = append(b, "21")
	want := `--- a/x.xml
+++ b/x.xml
@@ -2,7 +2,7 @@
 2
 3
 4
-5
+five
 6
 7
 8
@@ -18,3 +18,4 @@
 18
 19
 20
+21
`
	if got := Unified("a/x.xml", "b/x.xml", a, b); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if Unified("a", "b", a, a) != "" {
		t.Fatal("equal inputs must give empty diff")
	}
	got := Unified("a", "b", nil, L("x"))
	if got != "--- a\n+++ b\n@@ -0,0 +1 @@\n+x\n" {
		t.Fatalf("new file diff:\n%s", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/textdiff/`
Expected: FAIL, undefined `Lines`.

- [ ] **Step 3: Implement** `internal/textdiff/lcs.go`

```go
// Package textdiff implements the line-based diff, diff3 and unified output.
package textdiff

import "strings"

func Lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// Matches returns, for each line of a, the index of the matched line in b
// under a longest common subsequence, or -1.
func Matches(a, b []string) []int {
	m := make([]int, len(a))
	for i := range m {
		m[i] = -1
	}
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		m[pre] = pre
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		m[len(a)-1-suf] = len(b) - 1 - suf
		suf++
	}
	x, y := a[pre:len(a)-suf], b[pre:len(b)-suf]
	n, k := len(x), len(y)
	// t[i][j] = LCS length of x[i:], y[j:]
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, k+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			if x[i] == y[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	for i, j := 0, 0; i < n && j < k; {
		switch {
		case x[i] == y[j]:
			m[pre+i] = pre + j
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			i++
		default:
			j++
		}
	}
	return m
}
```

`internal/textdiff/diff3.go`:

```go
package textdiff

import "slices"

type Chunk struct {
	Conflict             bool
	Lines                []string // clean chunk content
	Local, Base, Remote []string // conflict sides
}

func Merge3(base, local, remote []string) []Chunk {
	ma, mb := Matches(base, local), Matches(base, remote)
	var out []Chunk
	io, ia, ib := 0, 0, 0
	for {
		k := io
		for k < len(base) && (ma[k] < 0 || mb[k] < 0) {
			k++
		}
		if k < len(base) && k == io && ma[k] == ia && mb[k] == ib {
			out = appendClean(out, base[k:k+1])
			io, ia, ib = io+1, ia+1, ib+1
			continue
		}
		ea, eb := len(local), len(remote)
		if k < len(base) {
			ea, eb = ma[k], mb[k]
		}
		o, a, b := base[io:k], local[ia:ea], remote[ib:eb]
		switch {
		case len(o)+len(a)+len(b) == 0:
		case slices.Equal(a, o):
			out = appendClean(out, b)
		case slices.Equal(b, o), slices.Equal(a, b):
			out = appendClean(out, a)
		default:
			out = append(out, Chunk{Conflict: true, Local: a, Base: o, Remote: b})
		}
		if k >= len(base) {
			return out
		}
		io, ia, ib = k, ea, eb
	}
}

func appendClean(out []Chunk, lines []string) []Chunk {
	if len(lines) == 0 {
		return out
	}
	if n := len(out); n > 0 && !out[n-1].Conflict {
		out[n-1].Lines = append(out[n-1].Lines, lines...)
		return out
	}
	return append(out, Chunk{Lines: append([]string(nil), lines...)})
}

func Render(chunks []Chunk, remoteLabel string) ([]string, int) {
	var out []string
	n := 0
	for _, c := range chunks {
		if !c.Conflict {
			out = append(out, c.Lines...)
			continue
		}
		n++
		out = append(out, "<<<<<<< local")
		out = append(out, c.Local...)
		out = append(out, "||||||| base")
		out = append(out, c.Base...)
		out = append(out, "=======")
		out = append(out, c.Remote...)
		out = append(out, ">>>>>>> "+remoteLabel)
	}
	return out, n
}
```

`internal/textdiff/unified.go`:

```go
package textdiff

import (
	"fmt"
	"strings"
)

type op struct {
	kind byte // ' ', '-', '+'
	line string
	ai, bi int // position in a / b before this op
}

func script(a, b []string) []op {
	m := Matches(a, b)
	var ops []op
	j := 0
	for i := range a {
		if m[i] < 0 {
			ops = append(ops, op{'-', a[i], i, j})
			continue
		}
		for ; j < m[i]; j++ {
			ops = append(ops, op{'+', b[j], i, j})
		}
		ops = append(ops, op{' ', a[i], i, j})
		j++
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j], len(a), j})
	}
	return ops
}

func rng(start, n int) string {
	s := start + 1
	if n == 0 {
		s = start
	}
	if n == 1 {
		return fmt.Sprintf("%d", s)
	}
	return fmt.Sprintf("%d,%d", s, n)
}

const context = 3

func Unified(aName, bName string, a, b []string) string {
	ops := script(a, b)
	var sb strings.Builder
	i := 0
	for i < len(ops) {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i == len(ops) {
			break
		}
		start := max(0, i-context)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*context {
				end = min(end+context, len(ops))
				break
			}
			end = run
		}
		if sb.Len() == 0 {
			fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
		}
		na, nb := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				na++
			}
			if o.kind != '-' {
				nb++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", rng(ops[start].ai, na), rng(ops[start].bi, nb))
		for _, o := range ops[start:end] {
			sb.WriteByte(o.kind)
			sb.WriteString(o.line)
			sb.WriteByte('\n')
		}
		i = end
	}
	return sb.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/textdiff/ -v`
Expected: PASS. If the "adjacent changes conflict" case merges cleanly, the stable-line condition in `Merge3` is wrong: a line counts as stable only when matched in **both** local and remote at the current positions.

- [ ] **Step 5: Commit**

```bash
git add internal/textdiff
git commit -m "textdiff: LCS, diff3 with git markers, unified diff"
```

---
### Task 6: `merge` — structural + textual three-way merge

Spec section 8. Inputs are resource roots (`<page>`), not whole files.

Algorithm:

1. Clone and `canon.Normalize` all three sides.
2. **Group** root children by key: `Field`/`List`/`Body`/unknown → element name (all same-name elements form one group, so a repeated field is compared as a whole); `Sub` with identity → `name + "\x00" + id`; `Sub` without identity (new, local only) → `name + "\x00new\x00" + <index>`.
3. For each key (local keys first, then remote-only, then base-only), compare the group texts (each node printed with `xmltree.Print(n, 3)`, joined by `\n`, absent group = `""`): `L == B` → take R; `R == B` → take L; `L == R` → take L; else **textual pass**: `textdiff.Merge3` over the lines. Clean → parse the merged lines back as a fragment and take those nodes. Conflicted, or clean but not well-formed → a conflict element whose text is the rendered diff3 (for the not-well-formed case: one whole-group hunk local/base/remote).
4. Root attributes are taken from the remote (they are service-owned).
5. Conflict groups are placed by putting a placeholder node (the local group's first node, or the remote's if local deleted it) into the tree, normalising, then swapping the placeholder for an `xmltree.Raw` node holding the marker lines. The group's other nodes are not added.
6. **Validation**: if nothing conflicted, run `validate.Resource(merged, remote, s)`. On failure, every group that was merged textually becomes a whole-group conflict and the tree is rebuilt once. If no group was merged textually, return the validation error.
7. Result: clean → `Root`. Conflicted → `Text` = `xmltree.Print(mergedWithRaw, 2)` (the resource root at file depth 2, ready to splice between `envelope.Header` and `envelope.Footer`), `Elements` = number of conflicting groups, `Hunks` = total conflict hunks.

**Files:**
- Create: `internal/merge/merge.go`
- Test: `internal/merge/merge_test.go`

**Interfaces:**
- Consumes: `xmltree` (Task 1), `schema`, `canon` (Task 2), `validate.Resource` (Task 4), `textdiff.Lines/Merge3/Render` (Task 5).
- Produces:
  - `type Result struct{ Root *xmltree.Node; Text string; Elements, Hunks int }`
  - `func (r Result) Conflicted() bool` (`Elements > 0`)
  - `func Merge(base, local, remote *xmltree.Node, s *schema.Schema, remoteLabel string) (Result, error)` — `remoteLabel` like `"remote v8"`

- [ ] **Step 1: Write the failing test** `internal/merge/merge_test.go`

```go
package merge

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var s = &schema.Schema{
	Root: "page", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{"id", true}, {"version", true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{"type", false}}, BodyTypes: []string{"x"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{"id", true}, {"author", true}, {"created", true}}},
	},
}

func p(t *testing.T, x string) *xmltree.Node {
	t.Helper()
	n, err := xmltree.ParseString(x)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func run(t *testing.T, b, l, r string) Result {
	t.Helper()
	res, err := Merge(p(t, b), p(t, l), p(t, r), s, "remote v4")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCleanDifferentElements(t *testing.T) {
	res := run(t,
		`<page id="1" version="3"><title>T</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="3"><title>Local</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="4"><title>T</title><labels><label>a</label><label>r</label></labels></page>`)
	if res.Conflicted() {
		t.Fatalf("unexpected conflict:\n%s", res.Text)
	}
	got := xmltree.Print(res.Root, 0)
	for _, want := range []string{`version="4"`, "<title>Local</title>", "<label>r</label>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestSubResources(t *testing.T) {
	res := run(t,
		`<page id="1"><comment id="1" created="1">a</comment><comment id="2" created="2">b</comment></page>`,
		`<page id="1"><comment id="1" created="1">a</comment><comment>new local</comment></page>`,
		`<page id="1"><comment id="1" created="1">a</comment><comment id="2" created="2">b</comment><comment id="3" created="3">new remote</comment></page>`)
	if res.Conflicted() {
		t.Fatal(res.Text)
	}
	want := "<page id=\"1\">\n  <comment id=\"1\" created=\"1\">a</comment>\n  <comment id=\"3\" created=\"3\">new remote</comment>\n  <comment>new local</comment>\n</page>"
	if got := xmltree.Print(res.Root, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestDeleteVersusChangeConflicts(t *testing.T) {
	res := run(t,
		`<page id="1"><comment id="2" created="2">b</comment></page>`,
		`<page id="1"/>`,
		`<page id="1"><comment id="2" created="2">b edited</comment></page>`)
	if !res.Conflicted() || res.Elements != 1 {
		t.Fatalf("want 1 conflict, got %+v", res)
	}
}

func TestBodyTextualMergeClean(t *testing.T) {
	b := "<page id=\"1\"><body type=\"x\"><p>1</p><p>2</p><p>3</p></body></page>"
	l := "<page id=\"1\"><body type=\"x\"><p>one</p><p>2</p><p>3</p></body></page>"
	r := "<page id=\"1\"><body type=\"x\"><p>1</p><p>2</p><p>three</p></body></page>"
	res := run(t, b, l, r)
	if res.Conflicted() {
		t.Fatal(res.Text)
	}
	got := xmltree.Print(res.Root, 0)
	if !strings.Contains(got, "<p>one</p>") || !strings.Contains(got, "<p>three</p>") {
		t.Fatal(got)
	}
}

func TestConflictMarkersGolden(t *testing.T) {
	res := run(t,
		`<page id="1" version="3"><title>T</title><labels><label>a</label></labels></page>`,
		`<page id="1" version="3"><title>T</title><labels><label>a</label><label>local</label></labels></page>`,
		`<page id="1" version="4"><title>T</title><labels><label>a</label><label>remote</label></labels></page>`)
	want := `    <page id="1" version="4">
      <title>T</title>
      <labels>
        <label>a</label>
<<<<<<< local
        <label>local</label>
||||||| base
=======
        <label>remote</label>
>>>>>>> remote v4
      </labels>
    </page>`
	if res.Text != want || res.Elements != 1 || res.Hunks != 1 {
		t.Fatalf("got (%d el, %d hunks)\n%s\nwant\n%s", res.Elements, res.Hunks, res.Text, want)
	}
}

func TestCleanDiff3ButIllFormedConflicts(t *testing.T) {
	// Each side is well-formed; line-merging them crosses <b> and <i>.
	b := "<page id=\"1\"><body type=\"x\">a\nb\nc\nd\ne\nf\ng</body></page>"
	l := "<page id=\"1\"><body type=\"x\"><b>a\nb\nc\nd\ne</b>\nf\ng</body></page>"
	r := "<page id=\"1\"><body type=\"x\">a\nb\n<i>c\nd\ne\nf\ng</i></body></page>"
	res := run(t, b, l, r)
	if !res.Conflicted() || res.Hunks != 1 || !strings.Contains(res.Text, "<<<<<<< local") {
		t.Fatalf("want whole-body conflict, got %+v", res)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/merge/`
Expected: FAIL, undefined `Merge`.

- [ ] **Step 3: Implement** `internal/merge/merge.go`

```go
// Package merge implements the gfs three-way merge (spec section 8).
package merge

import (
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/textdiff"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// rootDepth is the depth of the resource root inside a canonical file.
const rootDepth = 2

type Result struct {
	Root            *xmltree.Node // merged root, nil when conflicted
	Text            string        // conflicted root with markers, printed at rootDepth
	Elements, Hunks int
}

func (r Result) Conflicted() bool { return r.Elements > 0 }

type grouping struct {
	keys  []string
	nodes map[string][]*xmltree.Node
}

func group(root *xmltree.Node, s *schema.Schema) grouping {
	g := grouping{nodes: map[string][]*xmltree.Node{}}
	newIdx := 0
	for _, c := range root.Elements() {
		key := c.Name
		if e := schema.Find(s.Elems, c.Name); e != nil && e.Kind == schema.Sub {
			if id, ok := c.Attr(e.ID); ok {
				key = c.Name + "\x00" + id
			} else {
				newIdx++
				key = c.Name + "\x00new\x00" + strconv.Itoa(newIdx)
			}
		}
		if _, seen := g.nodes[key]; !seen {
			g.keys = append(g.keys, key)
		}
		g.nodes[key] = append(g.nodes[key], c)
	}
	return g
}

func text(nodes []*xmltree.Node) string {
	parts := make([]string, len(nodes))
	for i, n := range nodes {
		parts[i] = xmltree.Print(n, rootDepth+1)
	}
	return strings.Join(parts, "\n")
}

func parseFragment(lines []string) ([]*xmltree.Node, error) {
	n, err := xmltree.ParseString("<fragment>" + strings.Join(lines, "\n") + "</fragment>")
	if err != nil {
		return nil, err
	}
	return n.Elements(), nil
}

func prep(n *xmltree.Node, s *schema.Schema) *xmltree.Node {
	c := n.Clone()
	canon.Normalize(c, s)
	return c
}

func Merge(base, local, remote *xmltree.Node, s *schema.Schema, remoteLabel string) (Result, error) {
	b, l, r := prep(base, s), prep(local, s), prep(remote, s)
	bg, lg, rg := group(b, s), group(l, s), group(r, s)
	var keys []string
	seen := map[string]bool{}
	for _, g := range []grouping{lg, rg, bg} {
		for _, k := range g.keys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	whole := map[string]bool{} // groups forced to a whole-group conflict
	for attempt := 0; ; attempt++ {
		out := &xmltree.Node{Kind: xmltree.Element, Name: r.Name, Attrs: append([]xmltree.Attr(nil), r.Attrs...)}
		raws := map[*xmltree.Node]*xmltree.Node{}
		var textual []string
		res := Result{}
		conflict := func(k string, lines []string, hunks int) {
			ph := firstOf(lg.nodes[k], rg.nodes[k]).Clone()
			out.Children = append(out.Children, ph)
			raws[ph] = &xmltree.Node{Kind: xmltree.Raw, Text: strings.Join(lines, "\n")}
			res.Elements++
			res.Hunks += hunks
		}
		for _, k := range keys {
			bt, lt, rt := text(bg.nodes[k]), text(lg.nodes[k]), text(rg.nodes[k])
			switch {
			case lt == bt:
				out.Children = append(out.Children, clones(rg.nodes[k])...)
			case rt == bt, lt == rt:
				out.Children = append(out.Children, clones(lg.nodes[k])...)
			default:
				bl, ll, rl := textdiff.Lines(bt), textdiff.Lines(lt), textdiff.Lines(rt)
				if !whole[k] {
					lines, n := textdiff.Render(textdiff.Merge3(bl, ll, rl), remoteLabel)
					if n > 0 {
						conflict(k, lines, n)
						continue
					}
					if nodes, err := parseFragment(lines); err == nil {
						out.Children = append(out.Children, nodes...)
						textual = append(textual, k)
						continue
					}
				}
				lines, n := textdiff.Render([]textdiff.Chunk{{Conflict: true, Local: ll, Base: bl, Remote: rl}}, remoteLabel)
				conflict(k, lines, n)
			}
		}
		canon.Normalize(out, s)
		if res.Conflicted() {
			for i, c := range out.Children {
				if raw, ok := raws[c]; ok {
					out.Children[i] = raw
				}
			}
			res.Text = xmltree.Print(out, rootDepth)
			return res, nil
		}
		err := validate.Resource(out, r, s)
		if err == nil {
			res.Root = out
			return res, nil
		}
		if len(textual) == 0 || attempt > 0 {
			return Result{}, err
		}
		for _, k := range textual {
			whole[k] = true
		}
	}
}

func firstOf(a, b []*xmltree.Node) *xmltree.Node {
	if len(a) > 0 {
		return a[0]
	}
	return b[0]
}

func clones(nodes []*xmltree.Node) []*xmltree.Node {
	out := make([]*xmltree.Node, len(nodes))
	for i, n := range nodes {
		out[i] = n.Clone()
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/merge/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/merge
git commit -m "merge: structural and textual three-way merge with validation"
```

---
### Task 7: `workdir` — working tree and `.gfs/` storage

Everything that touches the disk. No XML knowledge.

Formats:

- `config`: INI. `[section]` lines, `key = value` lines, `#` or `;` comments, blank lines ignored. Written with section `remote` first, `policy` second, others sorted; keys sorted within a section; one blank line between sections.
- `index`: optional first line `# cursor <value>`; then one line per resource: `<id>\t<version>\t<path>` (tab-separated, path last so it may contain spaces). Written sorted by path.
- `log`: one line per action, tab-separated: `<RFC3339>\t<verb>\t<path>[ -> <newpath>]\t<outcome>\t<detail>`. `gfs log` prints it with the tabs as two spaces.
- `lock`: created with `O_EXCL`, contains the PID; removed by the returned unlock func.
- Paths are slash-separated, relative to the tree root. `WriteFile` writes atomically (temp file in the same directory + rename) and creates parent directories. `Remove` and `Rename` prune directories left empty, up to (not including) the root. `Scan` returns every regular file outside `.gfs/`, sorted.

**Files:**
- Create: `internal/workdir/workdir.go`, `internal/workdir/config.go`, `internal/workdir/index.go`, `internal/workdir/base.go`, `internal/workdir/log.go`, `internal/workdir/lock.go`
- Test: `internal/workdir/workdir_test.go`

**Interfaces:**
- Produces:
  - `var ErrNotInTree = errors.New("not a gfs working tree (or any parent): .gfs not found")`
  - `type Tree struct{ Root string }` (absolute path)
  - `func Find(start string) (*Tree, error)`; `func Init(dir string, cfg *Config) (*Tree, error)` (fails if `dir` exists and is non-empty)
  - `func (t *Tree) Abs(rel string) string`; `func (t *Tree) Rel(path string) (string, error)` (accepts absolute or cwd-relative OS paths, returns slash path; error if outside the tree or inside `.gfs/`)
  - `func (t *Tree) ReadFile(rel string) ([]byte, error)`, `WriteFile(rel string, data []byte) error`, `Remove(rel string) error`, `Rename(from, to string) error`, `Exists(rel string) bool`, `Scan() ([]string, error)`
  - `func (t *Tree) ReadBase(rel string) ([]byte, error)`, `WriteBase(rel string, data []byte) error`, `RemoveBase(rel string) error`, `RenameBase(from, to string) error`
  - `type Config struct{ ... }` with `func NewConfig() *Config`, `func ParseConfig(data []byte) (*Config, error)`, `func (c *Config) Get(section, key string) string`, `Set(section, key, value string)`, `Section(name string) map[string]string` (copy), `Bytes() []byte`; `func (t *Tree) LoadConfig() (*Config, error)`, `SaveConfig(c *Config) error`
  - `type Entry struct{ ID, Version, Path string }`; `type Index struct{ Cursor string; ... }` with `ByID(id string) (Entry, bool)`, `ByPath(path string) (Entry, bool)`, `Put(e Entry)` (replaces by ID), `Delete(id string)`, `All() []Entry` (sorted by path); `func (t *Tree) LoadIndex() (*Index, error)`, `SaveIndex(ix *Index) error`
  - `type LogEntry struct{ At time.Time; Verb, Path, NewPath, Outcome, Detail string }`; `func (t *Tree) AppendLog(e LogEntry) error`; `func (t *Tree) ReadLog() ([]LogEntry, error)`; `func (e LogEntry) String() string` (display form, two spaces between fields)
  - `func (t *Tree) Lock() (unlock func(), err error)`

- [ ] **Step 1: Write the failing test** `internal/workdir/workdir_test.go`

```go
package workdir

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTree(t *testing.T) *Tree {
	t.Helper()
	cfg := NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestInitAndFind(t *testing.T) {
	tr := newTree(t)
	for _, p := range []string{".gfs/config", ".gfs/base", ".gfs/index", ".gfs/log"} {
		if _, err := os.Stat(filepath.Join(tr.Root, p)); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(tr.Root, "a", "b")
	os.MkdirAll(sub, 0o755)
	got, err := Find(sub)
	if err != nil || got.Root != tr.Root {
		t.Fatalf("Find = %v, %v", got, err)
	}
	if _, err := Find(t.TempDir()); err != ErrNotInTree {
		t.Fatalf("want ErrNotInTree, got %v", err)
	}
	if _, err := Init(tr.Root, NewConfig()); err == nil {
		t.Fatal("Init into non-empty dir must fail")
	}
}

func TestFilesScanRenamePrune(t *testing.T) {
	tr := newTree(t)
	must(t, tr.WriteFile("eng/Home.xml", []byte("h")))
	must(t, tr.WriteFile("eng/Home/Arch itecture.xml", []byte("a")))
	must(t, tr.WriteBase("eng/Home.xml", []byte("h")))
	files, err := tr.Scan()
	must(t, err)
	if !slices.Equal(files, []string{"eng/Home.xml", "eng/Home/Arch itecture.xml"}) {
		t.Fatal(files)
	}
	must(t, tr.Rename("eng/Home/Arch itecture.xml", "eng/Other/A.xml"))
	if tr.Exists("eng/Home") || !tr.Exists("eng/Other/A.xml") {
		t.Fatal("rename must prune empty dir and create target dir")
	}
	must(t, tr.Remove("eng/Other/A.xml"))
	if tr.Exists("eng/Other") {
		t.Fatal("remove must prune")
	}
	b, err := tr.ReadBase("eng/Home.xml")
	if err != nil || string(b) != "h" {
		t.Fatal("base")
	}
}

func TestRel(t *testing.T) {
	tr := newTree(t)
	if r, err := tr.Rel(filepath.Join(tr.Root, "eng", "x.xml")); err != nil || r != "eng/x.xml" {
		t.Fatal(r, err)
	}
	if _, err := tr.Rel(filepath.Join(tr.Root, ".gfs", "index")); err == nil {
		t.Fatal("inside .gfs must fail")
	}
	if _, err := tr.Rel(t.TempDir()); err == nil {
		t.Fatal("outside must fail")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	in := "# comment\n[policy]\ndelete = deny\n\n[remote]\nurl = confluence://h/ENG\nemail = a@b\n"
	c, err := ParseConfig([]byte(in))
	must(t, err)
	if c.Get("remote", "url") != "confluence://h/ENG" || c.Get("policy", "delete") != "deny" || c.Get("x", "y") != "" {
		t.Fatal("Get")
	}
	want := "[remote]\nemail = a@b\nurl = confluence://h/ENG\n\n[policy]\ndelete = deny\n"
	if got := string(c.Bytes()); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := ParseConfig([]byte("novalue\n")); err == nil {
		t.Fatal("want parse error")
	}
}

func TestIndexRoundTrip(t *testing.T) {
	tr := newTree(t)
	ix, err := tr.LoadIndex()
	must(t, err)
	ix.Cursor = "c1"
	ix.Put(Entry{"2", "5", "eng/b b.xml"})
	ix.Put(Entry{"1", "3", "eng/a.xml"})
	ix.Put(Entry{"1", "4", "eng/a.xml"})
	must(t, tr.SaveIndex(ix))
	got, err := tr.LoadIndex()
	must(t, err)
	if got.Cursor != "c1" || len(got.All()) != 2 {
		t.Fatalf("%+v", got.All())
	}
	if e, ok := got.ByPath("eng/b b.xml"); !ok || e.ID != "2" {
		t.Fatal("ByPath")
	}
	if e, ok := got.ByID("1"); !ok || e.Version != "4" {
		t.Fatal("ByID")
	}
	got.Delete("1")
	if _, ok := got.ByID("1"); ok {
		t.Fatal("Delete")
	}
}

func TestLog(t *testing.T) {
	tr := newTree(t)
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	must(t, tr.AppendLog(LogEntry{At: at, Verb: "update", Path: "eng/a.xml", Outcome: "ok", Detail: "v2 -> v3"}))
	must(t, tr.AppendLog(LogEntry{At: at, Verb: "move", Path: "a.xml", NewPath: "b c.xml", Outcome: "FAIL", Detail: "409"}))
	es, err := tr.ReadLog()
	must(t, err)
	if len(es) != 2 || es[1].NewPath != "b c.xml" || es[0].Detail != "v2 -> v3" {
		t.Fatalf("%+v", es)
	}
	if s := es[1].String(); s != "2026-09-23T10:00:00Z  move  a.xml -> b c.xml  FAIL  409" {
		t.Fatal(s)
	}
}

func TestLock(t *testing.T) {
	tr := newTree(t)
	unlock, err := tr.Lock()
	must(t, err)
	if _, err := tr.Lock(); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock2, err := tr.Lock()
	must(t, err)
	unlock2()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/workdir/`
Expected: FAIL, undefined `NewConfig`.

- [ ] **Step 3: Implement** `internal/workdir/workdir.go`

```go
// Package workdir owns the working tree on disk and the .gfs/ directory.
package workdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const Dir = ".gfs"

var ErrNotInTree = errors.New("not a gfs working tree (or any parent): .gfs not found")

type Tree struct{ Root string }

func Find(start string) (*Tree, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, Dir)); err == nil && st.IsDir() {
			return &Tree{Root: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotInTree
		}
		dir = parent
	}
}

func Init(dir string, cfg *Config) (*Tree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if ents, err := os.ReadDir(abs); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("%s exists and is not empty", dir)
	}
	t := &Tree{Root: abs}
	if err := os.MkdirAll(filepath.Join(abs, Dir, "base"), 0o755); err != nil {
		return nil, err
	}
	for _, f := range []string{"index", "log"} {
		if err := os.WriteFile(filepath.Join(abs, Dir, f), nil, 0o644); err != nil {
			return nil, err
		}
	}
	return t, t.SaveConfig(cfg)
}

func (t *Tree) gfs(name string) string { return filepath.Join(t.Root, Dir, name) }

func (t *Tree) Abs(rel string) string { return filepath.Join(t.Root, filepath.FromSlash(rel)) }

func (t *Tree) Rel(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(t.Root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the working tree", path)
	}
	r = filepath.ToSlash(r)
	if r == Dir || strings.HasPrefix(r, Dir+"/") {
		return "", fmt.Errorf("%s is inside %s/", path, Dir)
	}
	if r == "." {
		r = ""
	}
	return r, nil
}

func (t *Tree) ReadFile(rel string) ([]byte, error) { return os.ReadFile(t.Abs(rel)) }
func (t *Tree) WriteFile(rel string, data []byte) error { return writeAtomic(t.Abs(rel), data) }
func (t *Tree) Exists(rel string) bool {
	_, err := os.Stat(t.Abs(rel))
	return err == nil
}

func (t *Tree) Remove(rel string) error {
	if err := os.Remove(t.Abs(rel)); err != nil {
		return err
	}
	prune(filepath.Dir(t.Abs(rel)), t.Root)
	return nil
}

func (t *Tree) Rename(from, to string) error { return renameIn(t.Abs(from), t.Abs(to), t.Root) }

func (t *Tree) Scan() ([]string, error) {
	var out []string
	err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == filepath.Join(t.Root, Dir) {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			r, _ := filepath.Rel(t.Root, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".gfs-tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func renameIn(from, to, root string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	prune(filepath.Dir(from), root)
	return nil
}

// prune removes empty directories from dir upwards, stopping before root.
func prune(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
```

`internal/workdir/base.go`:

```go
package workdir

import (
	"os"
	"path/filepath"
)

func (t *Tree) baseRoot() string            { return t.gfs("base") }
func (t *Tree) basePath(rel string) string { return filepath.Join(t.baseRoot(), filepath.FromSlash(rel)) }

func (t *Tree) ReadBase(rel string) ([]byte, error)       { return os.ReadFile(t.basePath(rel)) }
func (t *Tree) WriteBase(rel string, data []byte) error   { return writeAtomic(t.basePath(rel), data) }
func (t *Tree) RenameBase(from, to string) error          { return renameIn(t.basePath(from), t.basePath(to), t.baseRoot()) }
func (t *Tree) RemoveBase(rel string) error {
	err := os.Remove(t.basePath(rel))
	if err == nil {
		prune(filepath.Dir(t.basePath(rel)), t.baseRoot())
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
```

`internal/workdir/config.go`:

```go
package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Config struct{ sections map[string]map[string]string }

func NewConfig() *Config { return &Config{sections: map[string]map[string]string{}} }

func ParseConfig(data []byte) (*Config, error) {
	c := NewConfig()
	sec := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && line[len(line)-1] == ']':
			sec = strings.TrimSpace(line[1 : len(line)-1])
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("config line %d: want key = value", n)
			}
			c.Set(sec, strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	return c, sc.Err()
}

func (c *Config) Get(section, key string) string { return c.sections[section][key] }

func (c *Config) Set(section, key, value string) {
	if c.sections[section] == nil {
		c.sections[section] = map[string]string{}
	}
	c.sections[section][key] = value
}

func (c *Config) Section(name string) map[string]string {
	out := map[string]string{}
	for k, v := range c.sections[name] {
		out[k] = v
	}
	return out
}

func (c *Config) Bytes() []byte {
	var names []string
	for n := range c.sections {
		if n != "remote" && n != "policy" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	names = append([]string{"remote", "policy"}, names...)
	var b bytes.Buffer
	for _, n := range names {
		kv := c.sections[n]
		if len(kv) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", n)
		keys := make([]string, 0, len(kv))
		for k := range kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", k, kv[k])
		}
	}
	return b.Bytes()
}

func (t *Tree) LoadConfig() (*Config, error) {
	data, err := os.ReadFile(t.gfs("config"))
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

func (t *Tree) SaveConfig(c *Config) error { return writeAtomic(t.gfs("config"), c.Bytes()) }
```

`internal/workdir/index.go`:

```go
package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Entry struct{ ID, Version, Path string }

type Index struct {
	Cursor string
	byID   map[string]Entry
}

func (ix *Index) ByID(id string) (Entry, bool) {
	e, ok := ix.byID[id]
	return e, ok
}

func (ix *Index) ByPath(path string) (Entry, bool) {
	for _, e := range ix.byID {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

func (ix *Index) Put(e Entry)       { ix.byID[e.ID] = e }
func (ix *Index) Delete(id string)  { delete(ix.byID, id) }

func (ix *Index) All() []Entry {
	out := make([]Entry, 0, len(ix.byID))
	for _, e := range ix.byID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (t *Tree) LoadIndex() (*Index, error) {
	data, err := os.ReadFile(t.gfs("index"))
	if err != nil {
		return nil, err
	}
	ix := &Index{byID: map[string]Entry{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if c, ok := strings.CutPrefix(line, "# cursor "); ok && n == 1 {
			ix.Cursor = c
			continue
		}
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf(".gfs/index line %d: want id<TAB>version<TAB>path", n)
		}
		ix.Put(Entry{f[0], f[1], f[2]})
	}
	return ix, sc.Err()
}

func (t *Tree) SaveIndex(ix *Index) error {
	var b bytes.Buffer
	if ix.Cursor != "" {
		fmt.Fprintf(&b, "# cursor %s\n", ix.Cursor)
	}
	for _, e := range ix.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.ID, e.Version, e.Path)
	}
	return writeAtomic(t.gfs("index"), b.Bytes())
}
```

`internal/workdir/log.go`:

```go
package workdir

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

type LogEntry struct {
	At                                    time.Time
	Verb, Path, NewPath, Outcome, Detail string
}

func (e LogEntry) paths() string {
	if e.NewPath != "" {
		return e.Path + " -> " + e.NewPath
	}
	return e.Path
}

func (e LogEntry) String() string {
	s := strings.Join([]string{e.At.Format(time.RFC3339), e.Verb, e.paths(), e.Outcome}, "  ")
	if e.Detail != "" {
		s += "  " + e.Detail
	}
	return s
}

func (t *Tree) AppendLog(e LogEntry) error {
	f, err := os.OpenFile(t.gfs("log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	clean := func(s string) string { return strings.NewReplacer("\t", " ", "\n", " ").Replace(s) }
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\n", e.At.Format(time.RFC3339), e.Verb, clean(e.paths()), e.Outcome, clean(e.Detail))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (t *Tree) ReadLog() ([]LogEntry, error) {
	f, err := os.Open(t.gfs("log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "\t", 5)
		if len(p) != 5 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, p[0])
		path, np, _ := strings.Cut(p[2], " -> ")
		out = append(out, LogEntry{At: at, Verb: p[1], Path: path, NewPath: np, Outcome: p[3], Detail: p[4]})
	}
	return out, sc.Err()
}
```

`internal/workdir/lock.go`:

```go
package workdir

import (
	"fmt"
	"os"
)

func (t *Tree) Lock() (func(), error) {
	p := t.gfs("lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another gfs command is running (remove %s if it is not)", p)
		}
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	return func() { os.Remove(p) }, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/workdir/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/workdir
git commit -m "workdir: working tree IO and .gfs config, base, index, log, lock"
```

---
### Task 8: `adapter` contract, registry, and the in-memory `fake` adapter

Spec section 10. The engine talks to remotes only through these types. `fake` is a test double for the engine tests (spec section 12, "Fake remotes"); it is never registered in the CLI.

Contract notes:

- `Resource.Root == nil` in a `Listing` means a **stub**: the engine compares `Version` with the index and calls `Fetch` only if it differs.
- `Apply` executes actions in order and returns one `Result` per action. If the remote version differs from `req.Lock`, it returns a single result for the first action with `Err` wrapping `ErrLock` and executes nothing.
- `Apply` for a `create` of the resource sets `Result.ID` to the new identity.
- `Describe` fills `Action.Class` (policy class, default = verb) and `Action.Detail` (the text shown by `status`, e.g. `create page under "Runbooks"`).
- `IsMove(model, from, to)`: `Tree` → paths differ; `Flat` → never; `DirTree` → directories differ.

**Files:**
- Create: `internal/adapter/adapter.go`, `internal/adapter/registry.go`, `internal/adapter/fake/fake.go`
- Test: `internal/adapter/registry_test.go`, `internal/adapter/fake/fake_test.go`

**Interfaces:**
- Consumes: `xmltree` (Task 1), `schema` (Task 2), `validate.FindSub` (Task 4).
- Produces (package `adapter`):
  - `type PathModel int` with `Tree`, `Flat`, `DirTree`; `func IsMove(m PathModel, from, to string) bool`
  - `type Resource struct{ ID, Version, Path, By, At string; Root *xmltree.Node }`
  - `type Action struct{ Verb, Class, Target, Group, Detail, From, To string; Params map[string]string }`
  - `type Verb struct{ Name, Class, Help string; Params []string }`
  - `type Listing struct{ Resources []Resource; Deleted []string; Full bool; Cursor string }` (`Full`: every resource in scope is listed, so index entries not listed were deleted)
  - `type ApplyRequest struct{ Local, Base *Resource; Actions []Action; Lock string; IDByPath func(path string) (string, bool) }`
  - `type Result struct{ Action Action; Err error; Code, ID, Detail string }`
  - `var ErrLock, ErrNotFound error`
  - `type Adapter interface{ Name() string; Schemes() []string; Schema() *schema.Schema; PathModel() PathModel; DefaultDir(u *url.URL) string; Verbs() []Verb; Describe(a *Action, local *Resource); Open(ctx context.Context, u *url.URL, cfg map[string]string) (Session, error) }`
  - `type Session interface{ List(ctx context.Context, cursor string) (Listing, error); Fetch(ctx context.Context, id string) (*Resource, error); Apply(ctx context.Context, req ApplyRequest) []Result; Check(ctx context.Context, req ApplyRequest) []Result; Close() error }`
  - `func Register(a Adapter)`, `func ForURL(raw string) (Adapter, *url.URL, error)`, `func All() []Adapter`
- Produces (package `fake`):
  - `type Adapter struct{ Remote *Remote }` implementing `adapter.Adapter` (scheme `fake`, root `<note>`, `PathModel` `Tree`, verb `publish` class `publish` param `channel`)
  - `func New() *Adapter`
  - `type Remote struct{ FailVerb map[string]error; LockFailures int; Calls []string; Published []string; ... }` with `Put(id, path, xml string)`, `Edit(id string, f func(root *xmltree.Node))` (bumps version, sets `by="bob"`), `Delete(id string)`, `Move(id, path string)`, `Get(id string) (*adapter.Resource, bool)`
  - `var Schema *schema.Schema`

Fake schema (use verbatim):

```go
var Schema = &schema.Schema{
	Root: "note", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "tags", Kind: schema.List, Item: "tag", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type"}}, BodyTypes: []string{"text/plain"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}
```

- [ ] **Step 1: Write the failing tests**

`internal/adapter/registry_test.go`:

```go
package adapter

import (
	"context"
	"net/url"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

type stub struct{}

func (stub) Name() string                         { return "stub" }
func (stub) Schemes() []string                    { return []string{"stub", "stub+x"} }
func (stub) Schema() *schema.Schema               { return nil }
func (stub) PathModel() PathModel                 { return Tree }
func (stub) DefaultDir(*url.URL) string           { return "stub" }
func (stub) Verbs() []Verb                        { return nil }
func (stub) Describe(*Action, *Resource)          {}
func (stub) Open(context.Context, *url.URL, map[string]string) (Session, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	Register(stub{})
	a, u, err := ForURL("stub+x://host/SPACE")
	if err != nil || a.Name() != "stub" || u.Host != "host" {
		t.Fatalf("%v %v %v", a, u, err)
	}
	if _, _, err := ForURL("nope://x"); err == nil {
		t.Fatal("unknown scheme must fail")
	}
}

func TestIsMove(t *testing.T) {
	cases := []struct {
		m        PathModel
		from, to string
		want     bool
	}{
		{Tree, "a/b.xml", "a/c.xml", true},
		{Tree, "a/b.xml", "a/b.xml", false},
		{Flat, "a/b.xml", "c/d.xml", false},
		{DirTree, "inbox/x.xml", "inbox/y.xml", false},
		{DirTree, "inbox/x.xml", "archive/x.xml", true},
	}
	for _, c := range cases {
		if got := IsMove(c.m, c.from, c.to); got != c.want {
			t.Errorf("IsMove(%v,%s,%s)=%v", c.m, c.from, c.to, got)
		}
	}
}
```

`internal/adapter/fake/fake_test.go`:

```go
package fake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func TestFakeCreateUpdateLock(t *testing.T) {
	a := New()
	ctx := context.Background()
	s, _ := a.Open(ctx, nil, nil)
	root, _ := xmltree.ParseString(`<note><title>T</title><comment>hi</comment></note>`)
	res := s.Apply(ctx, adapter.ApplyRequest{
		Local:   &adapter.Resource{Path: "n/a.xml", Root: root},
		Actions: []adapter.Action{{Verb: "create"}},
	})
	if len(res) != 1 || res[0].Err != nil || res[0].ID == "" {
		t.Fatalf("%+v", res)
	}
	got, err := s.Fetch(ctx, res[0].ID)
	if err != nil || got.Path != "n/a.xml" || got.Version != "1" {
		t.Fatalf("%+v %v", got, err)
	}
	if c := got.Root.Child("comment"); c == nil {
		t.Fatal("comment lost")
	} else if _, ok := c.Attr("id"); !ok {
		t.Fatal("comment id not assigned")
	}

	a.Remote.Edit(res[0].ID, func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	stale := s.Apply(ctx, adapter.ApplyRequest{Local: got, Base: got, Lock: "1",
		Actions: []adapter.Action{{Verb: "update", Group: "title"}}})
	if !errors.Is(stale[0].Err, adapter.ErrLock) {
		t.Fatalf("want ErrLock, got %+v", stale)
	}

	a.Remote.FailVerb = map[string]error{"delete": errors.New("403 forbidden")}
	r := s.Apply(ctx, adapter.ApplyRequest{Local: got, Base: got, Lock: "2", Actions: []adapter.Action{{Verb: "delete"}}})
	if r[0].Err == nil || !strings.Contains(r[0].Err.Error(), "403") {
		t.Fatalf("%+v", r)
	}
	l, _ := s.List(ctx, "")
	if !l.Full || len(l.Resources) != 1 {
		t.Fatalf("%+v", l)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/...`
Expected: FAIL, undefined `Register`, `New`.

- [ ] **Step 3: Implement** `internal/adapter/adapter.go`

```go
// Package adapter is the contract between the gfs engine and a service.
package adapter

import (
	"context"
	"errors"
	"net/url"
	"path"

	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type PathModel int

const (
	Tree    PathModel = iota // path is meaningful; any rename is a move
	Flat                     // path derived from fields; local rename is a no-op
	DirTree                  // directories meaningful, file name derived
)

func IsMove(m PathModel, from, to string) bool {
	switch m {
	case Tree:
		return from != to
	case DirTree:
		return path.Dir(from) != path.Dir(to)
	}
	return false
}

type Resource struct {
	ID, Version, Path string
	By, At            string        // last modifier and time, for <conflict>
	Root              *xmltree.Node // nil in a listing stub
}

type Action struct {
	Verb     string // create, update, delete, move, or an explicit verb
	Class    string // policy class; defaults to Verb
	Target   string // "" for the resource itself, else e.g. comment[id=7] or comment[2]
	Group    string // update of a field group: element name
	Detail   string // human-readable resolved action
	From, To string // move
	Params   map[string]string
}

type Verb struct {
	Name, Class, Help string
	Params            []string
}

type Listing struct {
	Resources []Resource
	Deleted   []string
	Full      bool
	Cursor    string
}

type ApplyRequest struct {
	Local    *Resource // content to make the remote look like; Path is the working path
	Base     *Resource // nil for create
	Actions  []Action
	Lock     string // remote version observed before Apply
	IDByPath func(path string) (string, bool)
}

type Result struct {
	Action Action
	Err    error
	Code   string // service error code, e.g. HTTP status
	ID     string // create: new identity
	Detail string // e.g. "v2 -> v3"
}

var (
	ErrLock     = errors.New("remote version changed")
	ErrNotFound = errors.New("not found on remote")
)

type Adapter interface {
	Name() string
	Schemes() []string
	Schema() *schema.Schema
	PathModel() PathModel
	DefaultDir(u *url.URL) string
	Verbs() []Verb
	Describe(a *Action, local *Resource)
	Open(ctx context.Context, u *url.URL, cfg map[string]string) (Session, error)
}

type Session interface {
	List(ctx context.Context, cursor string) (Listing, error)
	Fetch(ctx context.Context, id string) (*Resource, error)
	Apply(ctx context.Context, req ApplyRequest) []Result
	Check(ctx context.Context, req ApplyRequest) []Result
	Close() error
}
```

`internal/adapter/registry.go`:

```go
package adapter

import (
	"fmt"
	"net/url"
	"sync"
)

var (
	mu       sync.Mutex
	byScheme = map[string]Adapter{}
	all      []Adapter
)

func Register(a Adapter) {
	mu.Lock()
	defer mu.Unlock()
	all = append(all, a)
	for _, s := range a.Schemes() {
		byScheme[s] = a
	}
}

func ForURL(raw string) (Adapter, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	mu.Lock()
	a, ok := byScheme[u.Scheme]
	mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("no adapter for scheme %q", u.Scheme)
	}
	return a, u, nil
}

func All() []Adapter {
	mu.Lock()
	defer mu.Unlock()
	return append([]Adapter(nil), all...)
}
```

`internal/adapter/fake/fake.go`:

```go
// Package fake is an in-memory adapter for engine tests.
package fake

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var Schema = &schema.Schema{
	Root: "note", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "tags", Kind: schema.List, Item: "tag", Sorted: true},
		{Name: "body", Kind: schema.Body, Attrs: []schema.Attr{{Name: "type"}}, BodyTypes: []string{"text/plain"}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "created", ReadOnly: true}}},
	},
}

type record struct {
	path    string
	root    *xmltree.Node
	version int
	by      string
}

type Remote struct {
	FailVerb     map[string]error // keyed by verb, or verb+" "+target
	LockFailures int              // Apply returns ErrLock this many times
	Calls        []string         // "verb path" per executed action
	Published    []string         // "path channel"
	recs         map[string]*record
	seq          int
}

func (r *Remote) next() string { r.seq++; return strconv.Itoa(r.seq) }

func parse(x string) *xmltree.Node {
	n, err := xmltree.ParseString(x)
	if err != nil {
		panic(err)
	}
	return n
}

// Put stores a resource at version 1 (xml is the <note> root, without id/version).
func (r *Remote) Put(id, path, x string) {
	r.recs[id] = &record{path: path, root: parse(x), version: 1, by: "alice"}
}

func (r *Remote) Edit(id string, f func(root *xmltree.Node)) {
	rec := r.recs[id]
	f(rec.root)
	rec.version++
	rec.by = "bob"
}

func (r *Remote) Delete(id string)     { delete(r.recs, id) }
func (r *Remote) Move(id, path string) { r.recs[id].path = path; r.recs[id].version++ }

func (r *Remote) Get(id string) (*adapter.Resource, bool) {
	rec, ok := r.recs[id]
	if !ok {
		return nil, false
	}
	return r.resource(id, rec), true
}

func (r *Remote) resource(id string, rec *record) *adapter.Resource {
	root := rec.root.Clone()
	root.SetAttr("id", id)
	root.SetAttr("version", strconv.Itoa(rec.version))
	return &adapter.Resource{ID: id, Version: strconv.Itoa(rec.version), Path: rec.path,
		By: rec.by, At: "2026-09-23T12:00:00Z", Root: root}
}

type Adapter struct{ Remote *Remote }

func New() *Adapter { return &Adapter{Remote: &Remote{recs: map[string]*record{}}} }

func (*Adapter) Name() string                  { return "fake" }
func (*Adapter) Schemes() []string             { return []string{"fake"} }
func (*Adapter) Schema() *schema.Schema        { return Schema }
func (*Adapter) PathModel() adapter.PathModel  { return adapter.Tree }
func (*Adapter) DefaultDir(*url.URL) string    { return "fake" }
func (*Adapter) Verbs() []adapter.Verb {
	return []adapter.Verb{{Name: "publish", Class: "publish", Help: "publish the note to a channel", Params: []string{"channel"}}}
}

func (*Adapter) Describe(a *adapter.Action, _ *adapter.Resource) {
	a.Class = a.Verb
	switch {
	case a.Target != "":
		a.Detail = a.Verb + " " + a.Target
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	case a.Verb == "move":
		a.Detail = "move"
	default:
		a.Detail = a.Verb + " note"
	}
}

func (a *Adapter) Open(context.Context, *url.URL, map[string]string) (adapter.Session, error) {
	return session{a.Remote}, nil
}

type session struct{ r *Remote }

func (s session) Close() error { return nil }

func (s session) List(context.Context, string) (adapter.Listing, error) {
	l := adapter.Listing{Full: true}
	for id, rec := range s.r.recs {
		l.Resources = append(l.Resources, *s.r.resource(id, rec))
	}
	return l, nil
}

func (s session) Fetch(_ context.Context, id string) (*adapter.Resource, error) {
	res, ok := s.r.Get(id)
	if !ok {
		return nil, adapter.ErrNotFound
	}
	return res, nil
}

func (s session) Check(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var out []adapter.Result
	for _, a := range req.Actions {
		out = append(out, adapter.Result{Action: a, Err: s.fault(a)})
	}
	return out
}

func (s session) fault(a adapter.Action) error {
	if err := s.r.FailVerb[a.Verb+" "+a.Target]; err != nil {
		return err
	}
	return s.r.FailVerb[a.Verb]
}

var targetRe = regexp.MustCompile(`^(\w+)\[(?:id=([^\]]+)|(\d+))\]$`)

func (s session) Apply(_ context.Context, req adapter.ApplyRequest) []adapter.Result {
	var id string
	var rec *record
	if req.Base != nil {
		id = req.Base.ID
		rec = s.r.recs[id]
		if rec == nil {
			return []adapter.Result{{Action: req.Actions[0], Err: adapter.ErrNotFound, Code: "404"}}
		}
		if s.r.LockFailures > 0 || (req.Lock != "" && req.Lock != strconv.Itoa(rec.version)) {
			if s.r.LockFailures > 0 {
				s.r.LockFailures--
			}
			return []adapter.Result{{Action: req.Actions[0], Err: fmt.Errorf("fake: %w", adapter.ErrLock), Code: "409"}}
		}
	}
	var out []adapter.Result
	changed := false
	for _, a := range req.Actions {
		res := adapter.Result{Action: a}
		if err := s.fault(a); err != nil {
			res.Err, res.Code = err, "500"
			out = append(out, res)
			continue
		}
		s.r.Calls = append(s.r.Calls, a.Verb+" "+req.Local.Path)
		switch {
		case a.Verb == "create" && a.Target == "":
			id = s.r.next()
			root := req.Local.Root.Clone()
			for _, c := range root.ChildrenNamed("comment") {
				c.SetAttr("id", "c"+s.r.next())
				c.SetAttr("created", "2026-09-23T12:00:00Z")
			}
			root.DelAttr("id")
			root.DelAttr("version")
			rec = &record{path: req.Local.Path, root: root, version: 0, by: "me"}
			s.r.recs[id] = rec
			res.ID = id
		case a.Verb == "delete" && a.Target == "":
			delete(s.r.recs, id)
			out = append(out, res)
			return out
		case a.Verb == "move":
			rec.path = a.To
		case a.Verb == "update" && a.Target == "":
			replaceGroup(rec.root, req.Local.Root, a.Group)
		case a.Target != "":
			s.applySub(rec.root, req.Local.Root, a)
		case a.Verb == "publish":
			s.r.Published = append(s.r.Published, req.Local.Path+" "+a.Params["channel"])
		}
		changed = true
		out = append(out, res)
	}
	if changed && rec != nil {
		rec.version++
		rec.by = "me"
	}
	return out
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}

func (s session) applySub(dst, src *xmltree.Node, a adapter.Action) {
	m := targetRe.FindStringSubmatch(a.Target)
	if m == nil {
		return
	}
	name, id, nth := m[1], m[2], m[3]
	switch a.Verb {
	case "create":
		n, _ := strconv.Atoi(nth)
		i := 0
		for _, c := range src.ChildrenNamed(name) {
			if _, has := c.Attr("id"); has {
				continue
			}
			if i++; i == n {
				nc := c.Clone()
				nc.SetAttr("id", "c"+s.r.next())
				nc.SetAttr("created", "2026-09-23T12:00:00Z")
				dst.Children = append(dst.Children, nc)
			}
		}
	case "update":
		if old, nw := validate.FindSub(dst, name, "id", id), validate.FindSub(src, name, "id", id); old != nil && nw != nil {
			*old = *nw.Clone()
		}
	case "delete":
		old := validate.FindSub(dst, name, "id", id)
		var kept []*xmltree.Node
		for _, c := range dst.Children {
			if c != old {
				kept = append(kept, c)
			}
		}
		dst.Children = kept
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter
git commit -m "adapter: contract, registry, in-memory fake for tests"
```

---
### Task 9: `changes` — status and action resolution

Spec sections 3.2 and 5.1. Compares the working tree to base + index and resolves each changed file into adapter actions. Used by `status`, `diff`, `commit`.

Rules:

- Scan every working file. If it has conflict markers or an envelope `<conflict>` → `C` (identity from `index.ByPath`). Else parse with `envelope.Parse`; a parse error gives status `M` if the path is in the index else `A`, with `Err` set.
- Identity is the resource root's `schema.ID` attribute. With identity found in the index: base is `ReadBase(entry.Path)`; path differs → moved. Without identity → new (`A`). A second file carrying the same identity → `Err` "duplicate identity, also in <path>".
- A file is **unchanged** when its canonical content (`xmltree.Print` after `canon.Normalize`) equals the base's, its path equals the base path, and the envelope has no `action` and no `<errors>`. Unchanged files are omitted.
- Status letter precedence: `C` > `!` (envelope has `<errors>`) > `A` > `R` (moved) > `M`.
- Index entries whose identity was not seen in any file (including files that failed to parse but whose path is in the index) → `D` with action `delete`.
- Every non-`C` file with a parsed document is validated with `validate.Resource(local, baseRoot, schema)`; failures go to `Err` (commit refuses the file, status prints the error).
- `ResolveActions`: base nil → `[create]`. Envelope `action` set → `[{Verb: action, Params}]` (design decision 1). Else: one `update` per changed `Field`/`List`/`Body` group (`Group` = element name), then sub `create` (`name[n]`), sub `update` (`name[id=X]`), sub `delete` (`name[id=X]`), then `move` if `adapter.IsMove(model, basePath, path)`. A rename that is not a move adds no action; status shows `R` and the file detail `rename ignored: name is derived`.
- Every action is passed through `Adapter.Describe`.
- `filter` (nil = all) keeps a change if it matches `Path` or `OldPath`. The CLI builds it from path arguments: a path matches if equal to, or under, an argument directory.

**Files:**
- Create: `internal/changes/changes.go`, `internal/changes/actions.go`
- Test: `internal/changes/changes_test.go`

**Interfaces:**
- Consumes: `workdir` (Task 7), `envelope` (Task 3), `canon`, `schema` (Task 2), `validate` (Task 4), `adapter`, `fake` (Task 8).
- Produces:
  - `type FileChange struct{ Status byte; Path, OldPath, ID string; Entry workdir.Entry; Local, Base *envelope.Doc; Actions []adapter.Action; Note string; Err error }` (`Entry` is the index entry, zero for `A`; `Note` e.g. the rename warning)
  - `func Compute(t *workdir.Tree, ix *workdir.Index, ad adapter.Adapter, filter func(string) bool) ([]FileChange, error)` — sorted by `Path`
  - `func ResolveActions(ad adapter.Adapter, base *xmltree.Node, local *envelope.Doc, basePath, path string) []adapter.Action`
  - `func CanonContent(root *xmltree.Node, s *schema.Schema) string` — canonical text of a resource root (clone, normalise, print at depth 0)
  - `func PathFilter(t *workdir.Tree, args []string) (func(string) bool, error)` — nil func when `args` is empty

- [ ] **Step 1: Write the failing test** `internal/changes/changes_test.go`

```go
package changes

import (
	"path/filepath"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

const note1 = `<note id="1" version="2"><title>T</title><comment id="c1" created="2026-01-01">hi</comment></note>`

func file(t *testing.T, x string) []byte {
	t.Helper()
	d, err := envelope.Parse([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	return envelope.Bytes(d, fake.Schema)
}

// setup creates a tree whose base and working copy both hold note1 at a/n.xml.
func setup(t *testing.T) (*workdir.Tree, *workdir.Index) {
	t.Helper()
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", "fake://x")
	tr, err := workdir.Init(filepath.Join(t.TempDir(), "wt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.WriteFile("a/n.xml", file(t, note1))
	tr.WriteBase("a/n.xml", file(t, note1))
	ix, _ := tr.LoadIndex()
	ix.Put(workdir.Entry{ID: "1", Version: "2", Path: "a/n.xml"})
	return tr, ix
}

func compute(t *testing.T, tr *workdir.Tree, ix *workdir.Index) []FileChange {
	t.Helper()
	cs, err := Compute(tr, ix, fake.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func one(t *testing.T, cs []FileChange) FileChange {
	t.Helper()
	if len(cs) != 1 {
		t.Fatalf("want 1 change, got %d: %+v", len(cs), cs)
	}
	return cs[0]
}

func verbs(c FileChange) []string {
	var out []string
	for _, a := range c.Actions {
		v := a.Verb
		if a.Group != "" {
			v += ":" + a.Group
		}
		if a.Target != "" {
			v += ":" + a.Target
		}
		out = append(out, v)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnchanged(t *testing.T) {
	tr, ix := setup(t)
	// Reformatting and attribute reordering is not a change.
	tr.WriteFile("a/n.xml", []byte(`<gfs><content><note version="2" id="1">
<comment created="2026-01-01" id="c1">hi</comment><title>T</title></note></content></gfs>`))
	if cs := compute(t, tr, ix); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

func TestStatuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(tr *workdir.Tree)
		status byte
		verbs  []string
		err    bool
	}{
		{"title edit", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>New</title><comment id="c1" created="2026-01-01">hi</comment></note>`))
		}, 'M', []string{"update:title"}, false},
		{"new comment", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>T</title><comment id="c1" created="2026-01-01">hi</comment><comment>new</comment></note>`))
		}, 'M', []string{"create:comment[1]"}, false},
		{"comment removed", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="2"><title>T</title></note>`))
		}, 'M', []string{"delete:comment[id=c1]"}, false},
		{"moved", func(tr *workdir.Tree) { tr.Rename("a/n.xml", "b/n.xml") }, 'R', []string{"move"}, false},
		{"deleted", func(tr *workdir.Tree) { tr.Remove("a/n.xml") }, 'D', []string{"delete"}, false},
		{"conflict", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte("<gfs>\n  <content>\n<<<<<<< local\nx\n||||||| base\n=======\ny\n>>>>>>> remote v3\n"))
		}, 'C', nil, false},
		{"failed last commit", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte(`<gfs action="publish" channel="x"><errors><error action="publish" code="500"><msg>boom</msg></error></errors><content>`+note1+`</content></gfs>`))
		}, '!', []string{"publish"}, false},
		{"explicit verb", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", []byte(`<gfs action="publish" channel="x"><content>`+note1+`</content></gfs>`))
		}, 'M', []string{"publish"}, false},
		{"version edited", func(tr *workdir.Tree) {
			tr.WriteFile("a/n.xml", file(t, `<note id="1" version="9"><title>T</title><comment id="c1" created="2026-01-01">hi</comment></note>`))
		}, 'M', nil, true},
		{"not xml", func(tr *workdir.Tree) { tr.WriteFile("a/n.xml", []byte("oops")) }, 'M', nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, ix := setup(t)
			c.mutate(tr)
			ch := one(t, compute(t, tr, ix))
			if ch.Status != c.status {
				t.Fatalf("status %c, want %c (err %v)", ch.Status, c.status, ch.Err)
			}
			if (ch.Err != nil) != c.err {
				t.Fatalf("err = %v", ch.Err)
			}
			if !c.err && !eq(verbs(ch), c.verbs) {
				t.Fatalf("verbs %v, want %v", verbs(ch), c.verbs)
			}
		})
	}
}

func TestNewFileBareRoot(t *testing.T) {
	tr, ix := setup(t)
	tr.WriteFile("a/new.xml", []byte("<note><title>N</title></note>"))
	ch := one(t, compute(t, tr, ix))
	if ch.Status != 'A' || !eq(verbs(ch), []string{"create"}) || !ch.Local.Wrapped || ch.Err != nil {
		t.Fatalf("%+v", ch)
	}
	if ch.Actions[0].Detail != "create note" {
		t.Fatalf("Describe not applied: %q", ch.Actions[0].Detail)
	}
}

func TestDuplicateIdentity(t *testing.T) {
	tr, ix := setup(t)
	data, _ := tr.ReadFile("a/n.xml")
	tr.WriteFile("a/copy.xml", data)
	cs := compute(t, tr, ix)
	var dup *FileChange
	for i := range cs {
		if cs[i].Err != nil {
			dup = &cs[i]
		}
	}
	if dup == nil {
		t.Fatalf("want duplicate identity error: %+v", cs)
	}
}

func TestPathFilter(t *testing.T) {
	tr, _ := setup(t)
	f, err := PathFilter(tr, []string{filepath.Join(tr.Root, "a")})
	if err != nil || !f("a/n.xml") || f("b/n.xml") || f("ab/x.xml") {
		t.Fatal("PathFilter")
	}
	if f, _ := PathFilter(tr, nil); f != nil {
		t.Fatal("no args -> nil filter")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/changes/`
Expected: FAIL, undefined `Compute`.

- [ ] **Step 3: Implement** `internal/changes/actions.go`

```go
package changes

import (
	"fmt"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func CanonContent(root *xmltree.Node, s *schema.Schema) string {
	c := root.Clone()
	canon.Normalize(c, s)
	return xmltree.Print(c, 0)
}

func groupText(root *xmltree.Node, name string) string {
	var out string
	for _, c := range root.ChildrenNamed(name) {
		out += xmltree.Print(c, 0) + "\n"
	}
	return out
}

func subsByID(root *xmltree.Node, e *schema.Elem) (map[string]*xmltree.Node, []*xmltree.Node, []string) {
	byID := map[string]*xmltree.Node{}
	var fresh []*xmltree.Node
	var order []string
	for _, c := range root.ChildrenNamed(e.Name) {
		if id, ok := c.Attr(e.ID); ok {
			byID[id] = c
			order = append(order, id)
		} else {
			fresh = append(fresh, c)
		}
	}
	return byID, fresh, order
}

func ResolveActions(ad adapter.Adapter, base *xmltree.Node, local *envelope.Doc, basePath, path string) []adapter.Action {
	s := ad.Schema()
	res := &adapter.Resource{Path: path, Root: local.Content}
	var acts []adapter.Action
	switch {
	case base == nil:
		acts = []adapter.Action{{Verb: "create"}}
	case local.Action != "":
		acts = []adapter.Action{{Verb: local.Action, Params: local.Params}}
	default:
		b, l := base.Clone(), local.Content.Clone()
		canon.Normalize(b, s)
		canon.Normalize(l, s)
		var creates, updates, deletes []adapter.Action
		for i := range s.Elems {
			e := &s.Elems[i]
			if e.Kind != schema.Sub {
				if groupText(b, e.Name) != groupText(l, e.Name) {
					acts = append(acts, adapter.Action{Verb: "update", Group: e.Name})
				}
				continue
			}
			bs, _, border := subsByID(b, e)
			ls, fresh, _ := subsByID(l, e)
			for n := range fresh {
				creates = append(creates, adapter.Action{Verb: "create", Target: fmt.Sprintf("%s[%d]", e.Name, n+1)})
			}
			for _, id := range border {
				ln, ok := ls[id]
				switch {
				case !ok:
					deletes = append(deletes, adapter.Action{Verb: "delete", Target: fmt.Sprintf("%s[id=%s]", e.Name, id)})
				case xmltree.Print(ln, 0) != xmltree.Print(bs[id], 0):
					updates = append(updates, adapter.Action{Verb: "update", Target: fmt.Sprintf("%s[id=%s]", e.Name, id)})
				}
			}
		}
		acts = append(append(append(acts, creates...), updates...), deletes...)
		if adapter.IsMove(ad.PathModel(), basePath, path) {
			acts = append(acts, adapter.Action{Verb: "move", From: basePath, To: path})
		}
	}
	for i := range acts {
		ad.Describe(&acts[i], res)
	}
	return acts
}
```

`internal/changes/changes.go`:

```go
// Package changes computes gfs status: what changed and which remote actions follow.
package changes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type FileChange struct {
	Status        byte
	Path, OldPath string
	ID            string
	Entry         workdir.Entry
	Local, Base   *envelope.Doc
	Actions       []adapter.Action
	Note          string
	Err           error
}

func Compute(t *workdir.Tree, ix *workdir.Index, ad adapter.Adapter, filter func(string) bool) ([]FileChange, error) {
	s := ad.Schema()
	files, err := t.Scan()
	if err != nil {
		return nil, err
	}
	var out []FileChange
	seen := map[string]string{} // id -> path
	for _, p := range files {
		data, err := t.ReadFile(p)
		if err != nil {
			return nil, err
		}
		fc := FileChange{Path: p}
		byPath, inIndex := ix.ByPath(p)
		if envelope.HasMarkers(data) || envelope.HasConflictElement(data) {
			fc.Status, fc.ID, fc.Entry = 'C', byPath.ID, byPath
			if inIndex {
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			fc.Status, fc.Err = 'A', err
			if inIndex {
				fc.Status, fc.ID, fc.Entry = 'M', byPath.ID, byPath
				seen[byPath.ID] = p
			}
			out = append(out, fc)
			continue
		}
		fc.Local = doc
		id, _ := doc.Content.Attr(s.ID)
		entry, known := ix.ByID(id)
		if id != "" && known {
			if other, dup := seen[id]; dup {
				fc.Status, fc.Err = 'A', fmt.Errorf("duplicate identity %s=%q, also in %s", s.ID, id, other)
				out = append(out, fc)
				continue
			}
			seen[id] = p
			fc.ID, fc.Entry = id, entry
			bdata, err := t.ReadBase(entry.Path)
			if err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
			if fc.Base, err = envelope.Parse(bdata); err != nil {
				return nil, fmt.Errorf("base for %s: %w", entry.Path, err)
			}
		}
		moved := fc.Base != nil && entry.Path != p
		if fc.Base != nil && !moved && doc.Action == "" && len(doc.Errors) == 0 &&
			CanonContent(doc.Content, s) == CanonContent(fc.Base.Content, s) {
			continue // unchanged
		}
		switch {
		case len(doc.Errors) > 0:
			fc.Status = '!'
		case fc.Base == nil:
			fc.Status = 'A'
		case moved:
			fc.Status = 'R'
		default:
			fc.Status = 'M'
		}
		if moved {
			fc.OldPath = entry.Path
			if !adapter.IsMove(ad.PathModel(), entry.Path, p) {
				fc.Note = "rename ignored: name is derived"
			}
		}
		var baseContent *xmltree.Node
		basePath := ""
		if fc.Base != nil {
			baseContent, basePath = fc.Base.Content, entry.Path
		}
		fc.Err = validate.Resource(doc.Content, baseContent, s)
		fc.Actions = ResolveActions(ad, baseContent, doc, basePath, p)
		out = append(out, fc)
	}
	for _, e := range ix.All() {
		if _, ok := seen[e.ID]; ok {
			continue
		}
		fc := FileChange{Status: 'D', Path: e.Path, ID: e.ID, Entry: e, Actions: []adapter.Action{{Verb: "delete"}}}
		if bdata, err := t.ReadBase(e.Path); err == nil {
			fc.Base, _ = envelope.Parse(bdata)
		}
		ad.Describe(&fc.Actions[0], nil)
		out = append(out, fc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if filter == nil {
		return out, nil
	}
	var kept []FileChange
	for _, c := range out {
		if filter(c.Path) || (c.OldPath != "" && filter(c.OldPath)) {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

func PathFilter(t *workdir.Tree, args []string) (func(string) bool, error) {
	if len(args) == 0 {
		return nil, nil
	}
	var rels []string
	for _, a := range args {
		r, err := t.Rel(a)
		if err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return func(p string) bool {
		for _, r := range rels {
			if r == "" || p == r || strings.HasPrefix(p, r+"/") {
				return true
			}
		}
		return false
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/changes/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/changes
git commit -m "changes: status computation and action resolution"
```

---
### Task 10: `policy`

Spec section 5.3.

**Files:**
- Create: `internal/policy/policy.go`
- Test: `internal/policy/policy_test.go`

**Interfaces:**
- Produces:
  - `type Policy struct{ ... }`; `func FromConfig(section map[string]string) (*Policy, error)` (the `[policy]` section; error on a value other than `allow|ask|deny`)
  - `func (p *Policy) Level(class string) string` — `allow`, `ask` or `deny`; defaults: `send`, `delete`, `publish` → `ask`, everything else `allow`
  - `type Decider struct{ Policy *Policy; Allowed map[string]bool; Prompt func(question string) bool }` (`Prompt == nil` means non-TTY)
  - `func (d *Decider) Decide(class, question string) (run bool, reason string)` — reasons: `""` when run, `"denied by policy"`, `"needs confirmation: rerun with --allow <class>"` (non-TTY ask), `"declined"` (TTY answered no)

- [ ] **Step 1: Write the failing test** `internal/policy/policy_test.go`

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/policy/`
Expected: FAIL, undefined `FromConfig`.

- [ ] **Step 3: Implement** `internal/policy/policy.go`

```go
// Package policy decides whether an action class may run (spec 5.3).
package policy

import "fmt"

const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

var defaults = map[string]string{"send": Ask, "delete": Ask, "publish": Ask}

type Policy struct{ levels map[string]string }

func FromConfig(section map[string]string) (*Policy, error) {
	p := &Policy{levels: map[string]string{}}
	for k, v := range defaults {
		p.levels[k] = v
	}
	for k, v := range section {
		if v != Allow && v != Ask && v != Deny {
			return nil, fmt.Errorf("[policy] %s = %q: want allow, ask or deny", k, v)
		}
		p.levels[k] = v
	}
	return p, nil
}

func (p *Policy) Level(class string) string {
	if l, ok := p.levels[class]; ok {
		return l
	}
	return Allow
}

type Decider struct {
	Policy  *Policy
	Allowed map[string]bool          // --allow <class>
	Prompt  func(question string) bool // nil: not a TTY
}

func (d *Decider) Decide(class, question string) (bool, string) {
	switch d.Policy.Level(class) {
	case Deny:
		return false, "denied by policy"
	case Ask:
		if d.Allowed[class] {
			return true, ""
		}
		if d.Prompt == nil {
			return false, "needs confirmation: rerun with --allow " + class
		}
		if !d.Prompt(question) {
			return false, "declined"
		}
	}
	return true, ""
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/policy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/policy
git commit -m "policy: allow/ask/deny with --allow and non-TTY deny"
```

---
### Task 11: `engine` — environment, write-back helpers, `Clone`

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/clone.go`
- Test: `internal/engine/engine_test.go` (shared helpers for Tasks 12–14 live here too)

**Interfaces:**
- Consumes: everything from Tasks 1–10.
- Produces:
  - `type Env struct{ Tree *workdir.Tree; Index *workdir.Index; Adapter adapter.Adapter; Session adapter.Session; Out io.Writer; Prompt func(string) bool; Now func() time.Time }`
  - `func (e *Env) Canonical(root *xmltree.Node) []byte` — bare canonical file bytes for a resource root
  - `func (e *Env) Store(res *adapter.Resource, oldPath string) error` — writes `res` canonical to working tree **and** base at `res.Path`, updates index (`ID`, `Version`, `Path`); if `oldPath != ""` and differs, removes the old working file and old base
  - `func (e *Env) StoreBase(res *adapter.Resource, oldPath string) error` — same, but base + index only (working file untouched)
  - `func (e *Env) Forget(id, path string) error` — removes working file (if present), base and index entry
  - `func (e *Env) Log(verb, path, newPath, outcome, detail string)` — appends to `.gfs/log` with `e.Now()`
  - `func (e *Env) IDByPath(path string) (string, bool)`
  - `func (e *Env) Resolved(ctx context.Context, r adapter.Resource) (*adapter.Resource, error)` — returns `r` if `r.Root != nil`, else `Session.Fetch(r.ID)`
  - `func Clone(ctx context.Context, ad adapter.Adapter, sess adapter.Session, rawURL, dir string, out io.Writer) (*Env, error)`

- [ ] **Step 1: Write the failing test** `internal/engine/engine_test.go`

```go
package engine

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
)

var ctx = context.Background()

// cloned returns an Env cloned from a fake remote holding two notes.
func cloned(t *testing.T) (*Env, *fake.Adapter, *bytes.Buffer) {
	t.Helper()
	ad := fake.New()
	ad.Remote.Put("1", "a/one.xml", `<note><title>One</title><comment id="c1" created="2026-01-01">hi</comment></note>`)
	ad.Remote.Put("2", "a/b/two.xml", `<note><title>Two</title><body type="text/plain">line1
line2
line3
line4
line5</body></note>`)
	sess, _ := ad.Open(ctx, nil, nil)
	var out bytes.Buffer
	env, err := Clone(ctx, ad, sess, "fake://x", filepath.Join(t.TempDir(), "wt"), &out)
	if err != nil {
		t.Fatal(err)
	}
	env.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	out.Reset()
	return env, ad, &out
}

func status(t *testing.T, env *Env) []changes.FileChange {
	t.Helper()
	cs, err := changes.Compute(env.Tree, env.Index, env.Adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func read(t *testing.T, env *Env, p string) string {
	t.Helper()
	b, err := env.Tree.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, env *Env, p, s string) {
	t.Helper()
	if err := env.Tree.WriteFile(p, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

// edit replaces old with new in a working file.
func edit(t *testing.T, env *Env, p, old, new string) {
	t.Helper()
	s := read(t, env, p)
	if !strings.Contains(s, old) {
		t.Fatalf("%s does not contain %q:\n%s", p, old, s)
	}
	write(t, env, p, strings.Replace(s, old, new, 1))
}

func TestClone(t *testing.T) {
	env, _, _ := cloned(t)
	one := read(t, env, "a/one.xml")
	want := `<?xml version="1.0" encoding="UTF-8"?>
<gfs>
  <content>
    <note id="1" version="1">
      <title>One</title>
      <comment id="c1" created="2026-01-01">hi</comment>
    </note>
  </content>
</gfs>
`
	if one != want {
		t.Fatalf("got\n%s", one)
	}
	if e, ok := env.Index.ByID("2"); !ok || e.Path != "a/b/two.xml" || e.Version != "1" {
		t.Fatalf("index: %+v", e)
	}
	if cs := status(t, env); len(cs) != 0 {
		t.Fatalf("clean clone must have no changes: %+v", cs)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/`
Expected: FAIL, undefined `Clone`.

- [ ] **Step 3: Implement** `internal/engine/engine.go`

```go
// Package engine implements clone, commit, pull and resolve (spec 3, 6, 7).
package engine

import (
	"context"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type Env struct {
	Tree    *workdir.Tree
	Index   *workdir.Index
	Adapter adapter.Adapter
	Session adapter.Session
	Out     io.Writer
	Prompt  func(string) bool // nil: not a TTY
	Now     func() time.Time
}

func (e *Env) Canonical(root *xmltree.Node) []byte {
	return envelope.Bytes(envelope.New(root.Clone()), e.Adapter.Schema())
}

func (e *Env) Store(res *adapter.Resource, oldPath string) error {
	if err := e.Tree.WriteFile(res.Path, e.Canonical(res.Root)); err != nil {
		return err
	}
	if oldPath != "" && oldPath != res.Path && e.Tree.Exists(oldPath) {
		if err := e.Tree.Remove(oldPath); err != nil {
			return err
		}
	}
	return e.StoreBase(res, oldPath)
}

func (e *Env) StoreBase(res *adapter.Resource, oldPath string) error {
	if oldPath != "" && oldPath != res.Path {
		if err := e.Tree.RemoveBase(oldPath); err != nil {
			return err
		}
	}
	if err := e.Tree.WriteBase(res.Path, e.Canonical(res.Root)); err != nil {
		return err
	}
	e.Index.Put(workdir.Entry{ID: res.ID, Version: res.Version, Path: res.Path})
	return e.Tree.SaveIndex(e.Index)
}

func (e *Env) Forget(id, path string) error {
	if e.Tree.Exists(path) {
		if err := e.Tree.Remove(path); err != nil {
			return err
		}
	}
	if err := e.Tree.RemoveBase(path); err != nil {
		return err
	}
	e.Index.Delete(id)
	return e.Tree.SaveIndex(e.Index)
}

func (e *Env) Log(verb, path, newPath, outcome, detail string) {
	_ = e.Tree.AppendLog(workdir.LogEntry{At: e.Now(), Verb: verb, Path: path, NewPath: newPath, Outcome: outcome, Detail: detail})
}

func (e *Env) IDByPath(path string) (string, bool) {
	en, ok := e.Index.ByPath(path)
	return en.ID, ok
}

func (e *Env) Resolved(ctx context.Context, r adapter.Resource) (*adapter.Resource, error) {
	if r.Root != nil {
		return &r, nil
	}
	return e.Session.Fetch(ctx, r.ID)
}
```

`internal/engine/clone.go`:

```go
package engine

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func Clone(ctx context.Context, ad adapter.Adapter, sess adapter.Session, rawURL, dir string, out io.Writer) (*Env, error) {
	cfg := workdir.NewConfig()
	cfg.Set("remote", "url", rawURL)
	t, err := workdir.Init(dir, cfg)
	if err != nil {
		return nil, err
	}
	ix, err := t.LoadIndex()
	if err != nil {
		return nil, err
	}
	env := &Env{Tree: t, Index: ix, Adapter: ad, Session: sess, Out: out, Now: time.Now}
	l, err := sess.List(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, r := range l.Resources {
		res, err := env.Resolved(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", r.ID, err)
		}
		if err := env.Store(res, ""); err != nil {
			return nil, err
		}
	}
	ix.Cursor = l.Cursor
	if err := t.SaveIndex(ix); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Cloned %d resources from %s into %s\n", len(l.Resources), rawURL, dir)
	return env, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/engine/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "engine: env, write-back helpers, clone"
```

---
### Task 12: `engine.Commit`

Spec section 6. The largest task; read the spec section once more before starting.

Behaviour, per file, in category order **creates (`A`), updates (`M`, `!`), moves (`R`), deletes (`D`)**, files independent of each other:

1. `C` → report `  C  <path>   unresolved conflict ...`, count a conflict, skip. `Err != nil` → report `invalid <path> FAIL <err>`, count failed, skip. No actions but a `Note` → print `warning: <path>: <note>`, skip.
2. **Policy, whole file**: every action is decided with `Decider.Decide(a.Class, "<verb> <path> ?")`. If any is refused, the whole file is skipped (so the file stays exactly as the user left it and `status` keeps showing it), reported as `denied` with the reason, counted in `Denied`, logged `denied`.
3. **Fetch** (not for `A`): `Session.Fetch(ID)`. `ErrNotFound` → fail every action with "deleted on remote". Canonical remote content ≠ canonical base content → remote moved:
   - `--no-merge`: write the file with a `<conflict>` summary element (elements="0" hunks="0"), content unchanged, base **not** updated; count conflict.
   - otherwise `merge.Merge(base, local, remote)`. Conflicted → write `envelope.Header(doc with Conflict) + res.Text + "\n" + envelope.Footer`, store the remote as base at the **local** path (design decision 4), count conflict, report `  C  <path>   conflict with remote v<N> by <by> <at>, <e> element(s), <h> hunk(s)`. Clean → continue with merged content; successful actions report `merged` instead of `ok`.
4. **Apply** with `Lock` = remote version, `Base` = remote resource, `Local` = `{ID, Path: working path, Root: content}`. First result wraps `ErrLock` → back to step 3 once; second time it is a failure.
5. **Write-back**: if no action succeeded → keep the user's document, set `<errors>` (one `<error>` per failed action), base untouched. If some succeeded → `Fetch` the (possibly new) ID, write the fetched resource canonical at the fetched `Path` (removing the old working file if the path changed); if some actions failed, first put the local form of the failed parts back into the written tree (`keepLocal`) and add `<errors>`, and keep the envelope `action`/params if the explicit verb failed; store the fetched resource (without the kept-local parts) as base. A failed `move` keeps the file at the working path.
6. Resource `delete` (status `D`): fetch; `ErrNotFound` → forget locally, report `ok  already deleted on remote`; remote changed since base → conflict `changed on remote since base; run gfs pull`; else apply (lock retry as above); success → `Forget`.
7. Every action result is reported (`<verb>  <path>[ -> <new>]   <ok|merged|FAIL|denied>  [<detail>]`) and logged. Footer: `<n> action(s), <n> failed, <n> denied`, plus `, <n> conflict(s)` when non-zero.
8. **Dry run**: no fetch/merge/apply/write. For each action print `<verb>  <path>   would run  [ask]|[deny]` using `Policy.Level`; run `Session.Check` and print `FAIL <err>` for failed checks. Counting as if it had run: `deny` → denied; `ask` without `--allow` on non-TTY → denied; failed check → failed; otherwise `Actions++`. Footer as normal, prefixed with `dry run: `.

Exit code: `Report.ExitCode()` is 1 if any failed, denied or conflicted, else 0. Errors *returned* by `Commit` are configuration/IO errors (exit 2).

**Files:**
- Create: `internal/engine/commit.go`
- Modify: `internal/changes/actions.go` (add `ParseTarget`)
- Test: `internal/engine/commit_test.go`

**Interfaces:**
- Consumes: Task 11 helpers; `changes.Compute`, `changes.CanonContent`; `merge.Merge`; `policy`; `envelope`.
- Produces:
  - `type CommitOpts struct{ DryRun bool; Allow map[string]bool; NoMerge bool; Filter func(string) bool }`
  - `type Report struct{ Actions, Failed, Denied, Conflicts int }`, `func (r Report) ExitCode() int`
  - `func Commit(ctx context.Context, e *Env, o CommitOpts) (Report, error)`
  - `func changes.ParseTarget(t string) (name, id string, nth int, ok bool)` — `comment[id=7]` → `("comment","7",0,true)`; `comment[2]` → `("comment","",2,true)`

- [ ] **Step 1: Write the failing test** `internal/engine/commit_test.go`

```go
package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func commit(t *testing.T, env *Env, o CommitOpts) Report {
	t.Helper()
	r, err := Commit(ctx, env, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func title(t *testing.T, env *Env, id string) string {
	t.Helper()
	res, err := env.Session.Fetch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return res.Root.Child("title").TextContent()
}

func TestCommitFastPath(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || r.Actions != 1 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(out.String(), "update  a/one.xml   ok") {
		t.Fatalf("report:\n%s", out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), `version="2"`) || len(status(t, env)) != 0 {
		t.Fatal("write-back must bump version and leave a clean status")
	}
	log, _ := env.Tree.ReadLog()
	if len(log) != 1 || log[0].Verb != "update" || log[0].Outcome != "ok" {
		t.Fatalf("log: %+v", log)
	}
}

func TestCommitCreateLenient(t *testing.T) {
	env, _, out := cloned(t)
	write(t, env, "a/new.xml", "<note><title>N</title><comment>first</comment></note>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/new.xml")
	if !strings.HasPrefix(got, "<?xml") || !strings.Contains(got, `<note id="`) || !strings.Contains(got, `<comment id="c`) {
		t.Fatalf("not written back canonical with ids:\n%s", got)
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status not clean after create")
	}
}

func TestCommitMerged(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("2", func(r *xmltree.Node) {
		b := r.Child("body")
		b.Children[0].Text = strings.Replace(b.Children[0].Text, "line5", "LINE5", 1)
	})
	edit(t, env, "a/b/two.xml", "<title>Two</title>", "<title>Dos</title>")
	r := commit(t, env, CommitOpts{})
	if r.ExitCode() != 0 || !strings.Contains(out.String(), "merged") {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/b/two.xml")
	if !strings.Contains(got, "<title>Dos</title>") || !strings.Contains(got, "LINE5") || len(status(t, env)) != 0 {
		t.Fatalf("merge not reflected:\n%s", got)
	}
}

func TestCommitConflict(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	r := commit(t, env, CommitOpts{})
	if r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	for _, want := range []string{`<conflict remote-version="2" by="bob"`, "<<<<<<< local", "<title>Local</title>", "||||||| base", "<title>One</title>", ">>>>>>> remote v2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if e, _ := env.Index.ByID("1"); e.Version != "2" {
		t.Fatal("base must move to the remote version")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != 'C' {
		t.Fatalf("%+v", cs)
	}
	if title(t, env, "1") != "Remote" {
		t.Fatal("remote must be untouched")
	}
}

func TestCommitNoMerge(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("2", func(r *xmltree.Node) { r.Child("title").Children[0].Text = "Remote" })
	edit(t, env, "a/b/two.xml", "line1", "LINE1")
	r := commit(t, env, CommitOpts{NoMerge: true})
	got := read(t, env, "a/b/two.xml")
	if r.Conflicts != 1 || strings.Contains(got, "<<<<<<<") || !strings.Contains(got, `<conflict remote-version="2"`) {
		t.Fatalf("%+v\n%s", r, got)
	}
	if e, _ := env.Index.ByID("2"); e.Version != "1" {
		t.Fatal("--no-merge must not move base")
	}
}

func TestCommitLockRetry(t *testing.T) {
	for _, c := range []struct {
		failures int
		exit     int
	}{{1, 0}, {2, 1}} {
		env, ad, out := cloned(t)
		ad.Remote.LockFailures = c.failures
		edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
		if r := commit(t, env, CommitOpts{}); r.ExitCode() != c.exit {
			t.Fatalf("failures=%d: %+v\n%s", c.failures, r, out)
		}
	}
}

func TestCommitPartialFailure(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.FailVerb = map[string]error{"create comment[1]": errors.New("comment service down")}
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	edit(t, env, "a/one.xml", "</note>", "<comment>retry me</comment></note>")
	r := commit(t, env, CommitOpts{})
	if r.Failed != 1 || r.Actions != 2 || title(t, env, "1") != "Uno" {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	if !strings.Contains(got, `target="comment[1]"`) || !strings.Contains(got, "<comment>retry me</comment>") || !strings.Contains(got, "<title>Uno</title>") {
		t.Fatalf("partial write-back wrong:\n%s", got)
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != '!' {
		t.Fatalf("%+v", cs)
	}
	ad.Remote.FailVerb = nil
	out.Reset()
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("retry: %+v\n%s", r, out)
	}
	if got := read(t, env, "a/one.xml"); strings.Contains(got, "<errors>") || !strings.Contains(got, "retry me</comment>") {
		t.Fatalf("retry write-back:\n%s", got)
	}
}

func TestCommitAllFailed(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.FailVerb = map[string]error{"update": errors.New("500 boom")}
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	r := commit(t, env, CommitOpts{})
	got := read(t, env, "a/one.xml")
	if r.Failed != 1 || r.ExitCode() != 1 || !strings.Contains(got, "<msg>500 boom</msg>") || !strings.Contains(got, "<title>Uno</title>") {
		t.Fatalf("%+v\n%s", r, got)
	}
	if e, _ := env.Index.ByID("1"); e.Version != "1" {
		t.Fatal("base must stay")
	}
}

func TestCommitDeletePolicy(t *testing.T) {
	env, ad, out := cloned(t)
	env.Tree.Remove("a/one.xml")
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 || r.ExitCode() != 1 {
		t.Fatalf("non-TTY ask must deny: %+v\n%s", r, out)
	}
	if _, ok := ad.Remote.Get("1"); !ok {
		t.Fatal("denied delete must not run")
	}
	env.Prompt = func(q string) bool { return strings.HasPrefix(q, "delete a/one.xml") }
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("TTY yes: %+v\n%s", r, out)
	}
	if _, ok := ad.Remote.Get("1"); ok {
		t.Fatal("delete did not run")
	}
	if _, ok := env.Index.ByID("1"); ok || len(status(t, env)) != 0 {
		t.Fatal("delete must forget the resource")
	}
}

func TestCommitExplicitVerb(t *testing.T) {
	env, ad, out := cloned(t)
	edit(t, env, "a/one.xml", "<gfs>", `<gfs action="publish" channel="#eng">`)
	if r := commit(t, env, CommitOpts{}); r.Denied != 1 {
		t.Fatalf("publish is ask: %+v", r)
	}
	if r := commit(t, env, CommitOpts{Allow: map[string]bool{"publish": true}}); r.ExitCode() != 0 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if len(ad.Remote.Published) != 1 || ad.Remote.Published[0] != "a/one.xml #eng" {
		t.Fatalf("%v", ad.Remote.Published)
	}
	if strings.Contains(read(t, env, "a/one.xml"), "action=") {
		t.Fatal("envelope must be bare after success")
	}
}

func TestCommitDryRun(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Uno</title>")
	env.Tree.Remove("a/b/two.xml")
	r := commit(t, env, CommitOpts{DryRun: true})
	if title(t, env, "1") != "One" || r.Actions != 1 || r.Denied != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(out.String(), "would run") || !strings.Contains(out.String(), "[ask]") {
		t.Fatalf("dry-run output:\n%s", out)
	}
}

func TestCommitInvalid(t *testing.T) {
	env, _, out := cloned(t)
	edit(t, env, "a/one.xml", `version="1"`, `version="7"`)
	if r := commit(t, env, CommitOpts{}); r.Failed != 1 || !strings.Contains(out.String(), "read-only") {
		t.Fatalf("%+v\n%s", r, out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run Commit`
Expected: FAIL, undefined `Commit`.

- [ ] **Step 3: Add `ParseTarget`** to `internal/changes/actions.go`

```go
var targetRe = regexp.MustCompile(`^([\w:.-]+)\[(?:id=([^\]]+)|(\d+))\]$`)

// ParseTarget splits "comment[id=7]" or "comment[2]".
func ParseTarget(t string) (name, id string, nth int, ok bool) {
	m := targetRe.FindStringSubmatch(t)
	if m == nil {
		return "", "", 0, false
	}
	if m[3] != "" {
		nth, _ = strconv.Atoi(m[3])
	}
	return m[1], m[2], nth, true
}
```

(add `regexp` and `strconv` to the imports.)

- [ ] **Step 4: Implement** `internal/engine/commit.go`

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type CommitOpts struct {
	DryRun  bool
	Allow   map[string]bool
	NoMerge bool
	Filter  func(string) bool
}

type Report struct{ Actions, Failed, Denied, Conflicts int }

func (r Report) ExitCode() int {
	if r.Failed+r.Denied+r.Conflicts > 0 {
		return 1
	}
	return 0
}

func category(fc changes.FileChange) int {
	switch fc.Status {
	case 'A':
		return 0
	case 'R':
		return 2
	case 'D':
		return 3
	}
	return 1
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func oneLine(err error) string { return strings.ReplaceAll(err.Error(), "\n", "; ") }

func (e *Env) line(verb, path, newPath, outcome, detail string) {
	p := path
	if newPath != "" && newPath != path {
		p += " -> " + newPath
	}
	s := fmt.Sprintf("%-7s %s   %s", verb, p, outcome)
	if detail != "" {
		s += "  " + detail
	}
	fmt.Fprintln(e.Out, s)
}

func (e *Env) policy() (*policy.Policy, error) {
	cfg, err := e.Tree.LoadConfig()
	if err != nil {
		return nil, err
	}
	return policy.FromConfig(cfg.Section("policy"))
}

func Commit(ctx context.Context, e *Env, o CommitOpts) (Report, error) {
	pol, err := e.policy()
	if err != nil {
		return Report{}, err
	}
	cs, err := changes.Compute(e.Tree, e.Index, e.Adapter, o.Filter)
	if err != nil {
		return Report{}, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return category(cs[i]) < category(cs[j]) })
	d := &policy.Decider{Policy: pol, Allowed: o.Allow, Prompt: e.Prompt}
	var r Report
	for _, fc := range cs {
		var err error
		if o.DryRun {
			e.dryRunFile(ctx, fc, d, &r)
		} else {
			err = e.commitFile(ctx, fc, d, o, &r)
		}
		if err != nil {
			return r, err
		}
	}
	footer := fmt.Sprintf("%s, %d failed, %d denied", plural(r.Actions, "action"), r.Failed, r.Denied)
	if r.Conflicts > 0 {
		footer += ", " + plural(r.Conflicts, "conflict")
	}
	if o.DryRun {
		footer = "dry run: " + footer
	}
	fmt.Fprintln(e.Out, footer)
	return r, nil
}

// precheck handles C, invalid and no-op files. It returns false when the file is done.
func (e *Env) precheck(fc changes.FileChange, r *Report) bool {
	switch {
	case fc.Status == 'C':
		r.Conflicts++
		fmt.Fprintf(e.Out, "  C  %s   unresolved conflict: edit, remove <conflict/>, commit (or gfs resolve)\n", fc.Path)
		return false
	case fc.Err != nil:
		r.Failed++
		e.line("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		e.Log("invalid", fc.Path, "", "FAIL", oneLine(fc.Err))
		return false
	case len(fc.Actions) == 0:
		if fc.Note != "" {
			fmt.Fprintf(e.Out, "warning: %s: %s\n", fc.Path, fc.Note)
		}
		return false
	}
	return true
}

func (e *Env) dryRunFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, r *Report) {
	if !e.precheck(fc, r) {
		return
	}
	var checks []adapter.Result
	if fc.Local != nil {
		checks = e.Session.Check(ctx, adapter.ApplyRequest{
			Local: &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: fc.Local.Content},
			Base:  baseResource(fc), Actions: fc.Actions, IDByPath: e.IDByPath,
		})
	}
	for i, a := range fc.Actions {
		level := d.Policy.Level(a.Class)
		mark := ""
		switch {
		case level == policy.Deny:
			mark = "[deny]"
			r.Denied++
		case level == policy.Ask && !d.Allowed[a.Class]:
			mark = "[ask]"
			if d.Prompt == nil {
				r.Denied++
			} else {
				r.Actions++
			}
		default:
			r.Actions++
		}
		if i < len(checks) && checks[i].Err != nil {
			r.Failed++
			e.line(a.Verb, fc.Path, a.To, "FAIL", oneLine(checks[i].Err))
			continue
		}
		e.line(a.Verb, fc.Path, a.To, "would run", strings.TrimSpace(a.Detail+"  "+mark))
	}
}

func baseResource(fc changes.FileChange) *adapter.Resource {
	if fc.Base == nil {
		return nil
	}
	return &adapter.Resource{ID: fc.ID, Version: fc.Entry.Version, Path: fc.Entry.Path, Root: fc.Base.Content}
}

func (e *Env) commitFile(ctx context.Context, fc changes.FileChange, d *policy.Decider, o CommitOpts, r *Report) error {
	if !e.precheck(fc, r) {
		return nil
	}
	for _, a := range fc.Actions {
		if run, reason := d.Decide(a.Class, fmt.Sprintf("%s %s ?", a.Verb, fc.Path)); !run {
			r.Denied++
			e.line(a.Verb, fc.Path, "", "denied", reason)
			e.Log(a.Verb, fc.Path, "", "denied", reason)
			return nil
		}
	}
	s := e.Adapter.Schema()
	for attempt := 0; ; attempt++ {
		var remote *adapter.Resource
		content := (*xmltree.Node)(nil)
		if fc.Local != nil {
			content = fc.Local.Content
		}
		merged := false
		if fc.Base != nil {
			var err error
			remote, err = e.Session.Fetch(ctx, fc.ID)
			if errors.Is(err, adapter.ErrNotFound) {
				if fc.Status == 'D' {
					r.Actions++
					e.line("delete", fc.Path, "", "ok", "already deleted on remote")
					e.Log("delete", fc.Path, "", "ok", "already deleted on remote")
					return e.Forget(fc.ID, fc.Path)
				}
				return e.failAll(fc, fc.Actions, errors.New("deleted on remote; run gfs pull"), r)
			}
			if err != nil {
				return e.failAll(fc, fc.Actions, err, r)
			}
			if changes.CanonContent(remote.Root, s) != changes.CanonContent(fc.Base.Content, s) {
				if fc.Status == 'D' {
					r.Conflicts++
					fmt.Fprintf(e.Out, "  C  %s   changed on remote since base; run gfs pull\n", fc.Path)
					return nil
				}
				if o.NoMerge {
					return e.refuse(fc, remote, r)
				}
				res, err := merge.Merge(fc.Base.Content, fc.Local.Content, remote.Root, s, "remote v"+remote.Version)
				if err != nil {
					return e.failAll(fc, fc.Actions, fmt.Errorf("merge: %w", err), r)
				}
				if res.Conflicted() {
					return e.writeConflict(fc, remote, res, r)
				}
				content, merged = res.Root, true
			}
		}
		local := &adapter.Resource{ID: fc.ID, Path: fc.Path, Root: content}
		if fc.Status == 'D' {
			local = remote
		}
		req := adapter.ApplyRequest{Local: local, Base: remote, Actions: fc.Actions, IDByPath: e.IDByPath}
		if remote != nil {
			req.Lock = remote.Version
		}
		results := e.Session.Apply(ctx, req)
		if len(results) > 0 && errors.Is(results[0].Err, adapter.ErrLock) && attempt == 0 {
			continue
		}
		return e.finish(ctx, fc, content, results, merged, r)
	}
}

func (e *Env) errorsFor(failed []adapter.Result) []envelope.Error {
	var out []envelope.Error
	for _, f := range failed {
		out = append(out, envelope.Error{Action: f.Action.Verb, Target: f.Action.Target, Code: f.Code,
			At: e.Now().Format(time.RFC3339), Msg: oneLine(f.Err)})
	}
	return out
}

func (e *Env) failAll(fc changes.FileChange, acts []adapter.Action, err error, r *Report) error {
	var results []adapter.Result
	for _, a := range acts {
		results = append(results, adapter.Result{Action: a, Err: err})
	}
	return e.finish(context.Background(), fc, nil, results, false, r)
}

func (e *Env) refuse(fc changes.FileChange, remote *adapter.Resource, r *Report) error {
	doc := *fc.Local
	doc.Conflict = &envelope.Conflict{RemoteVersion: remote.Version, By: remote.By, At: remote.At}
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   remote moved to v%s (--no-merge)\n", fc.Path, remote.Version)
	e.Log("merge", fc.Path, "", "FAIL", "remote moved to v"+remote.Version+" (--no-merge)")
	return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, e.Adapter.Schema()))
}

func (e *Env) writeConflict(fc changes.FileChange, remote *adapter.Resource, res merge.Result, r *Report) error {
	doc := &envelope.Doc{Action: fc.Local.Action, Params: fc.Local.Params, Conflict: &envelope.Conflict{
		RemoteVersion: remote.Version, By: remote.By, At: remote.At, Elements: res.Elements, Hunks: res.Hunks}}
	if err := e.Tree.WriteFile(fc.Path, []byte(envelope.Header(doc)+res.Text+"\n"+envelope.Footer)); err != nil {
		return err
	}
	base := *remote
	base.Path = fc.Path
	r.Conflicts++
	fmt.Fprintf(e.Out, "  C  %s   conflict with remote v%s by %s %s, %s, %s\n", fc.Path, remote.Version, remote.By, remote.At,
		plural(res.Elements, "element"), plural(res.Hunks, "hunk"))
	e.Log("merge", fc.Path, "", "FAIL", "conflict with remote v"+remote.Version)
	return e.StoreBase(&base, fc.Entry.Path)
}

func (e *Env) finish(ctx context.Context, fc changes.FileChange, content *xmltree.Node, results []adapter.Result, merged bool, r *Report) error {
	s := e.Adapter.Schema()
	var failed []adapter.Result
	id := fc.ID
	moveFailed, deleted := false, false
	for _, res := range results {
		r.Actions++
		if res.Err != nil {
			r.Failed++
			failed = append(failed, res)
			moveFailed = moveFailed || res.Action.Verb == "move"
			continue
		}
		if res.ID != "" {
			id = res.ID
		}
		if res.Action.Verb == "delete" && res.Action.Target == "" {
			deleted = true
		}
	}
	report := func(newPath string) {
		for _, res := range results {
			outcome, detail := "ok", res.Detail
			if merged {
				outcome = "merged"
			}
			if res.Err != nil {
				outcome, detail = "FAIL", oneLine(res.Err)
			}
			np := ""
			if res.Action.Verb == "move" || res.Err == nil {
				np = newPath
			}
			e.line(res.Action.Verb, fc.Path, np, outcome, strings.TrimSpace(res.Action.Target+" "+detail))
			e.Log(res.Action.Verb, fc.Path, np, outcome, strings.TrimSpace(res.Action.Target+" "+detail))
		}
	}
	if len(failed) == len(results) {
		report("")
		if fc.Local == nil { // deleted file: nothing to annotate
			return nil
		}
		doc := *fc.Local
		doc.Errors, doc.Conflict = e.errorsFor(failed), nil
		return e.Tree.WriteFile(fc.Path, envelope.Bytes(&doc, s))
	}
	if deleted {
		report("")
		return e.Forget(fc.ID, fc.Entry.Path)
	}
	remote, err := e.Session.Fetch(ctx, id)
	if err != nil {
		report("")
		r.Failed++
		e.line("fetch", fc.Path, "", "FAIL", "write-back: "+oneLine(err))
		return nil
	}
	wb := remote.Root.Clone()
	doc := envelope.New(wb)
	path := remote.Path
	if len(failed) > 0 {
		keepLocal(wb, content, failed, s)
		doc.Errors = e.errorsFor(failed)
		if fc.Local.Action != "" {
			doc.Action, doc.Params = fc.Local.Action, fc.Local.Params
		}
		if moveFailed {
			path = fc.Path
		}
	}
	if err := e.Tree.WriteFile(path, envelope.Bytes(doc, s)); err != nil {
		return err
	}
	if path != fc.Path && e.Tree.Exists(fc.Path) {
		if err := e.Tree.Remove(fc.Path); err != nil {
			return err
		}
	}
	report(path)
	return e.StoreBase(remote, fc.Entry.Path)
}

// keepLocal puts the local form of failed parts back into the written-back tree,
// so that a retry is a plain re-commit.
func keepLocal(wb, local *xmltree.Node, failed []adapter.Result, s *schema.Schema) {
	if local == nil {
		return
	}
	for _, f := range failed {
		a := f.Action
		if a.Target == "" {
			if a.Verb == "update" && a.Group != "" {
				replaceGroup(wb, local, a.Group)
			}
			continue
		}
		name, id, nth, ok := changes.ParseTarget(a.Target)
		e := schema.Find(s.Elems, name)
		if !ok || e == nil {
			continue
		}
		switch a.Verb {
		case "create":
			i := 0
			for _, c := range local.ChildrenNamed(name) {
				if _, has := c.Attr(e.ID); !has {
					if i++; i == nth {
						wb.Children = append(wb.Children, c.Clone())
					}
				}
			}
		case "update":
			if old, nw := validate.FindSub(wb, name, e.ID, id), validate.FindSub(local, name, e.ID, id); old != nil && nw != nil {
				*old = *nw.Clone()
			}
		case "delete":
			old := validate.FindSub(wb, name, e.ID, id)
			var kept []*xmltree.Node
			for _, c := range wb.Children {
				if c != old {
					kept = append(kept, c)
				}
			}
			wb.Children = kept
		}
	}
}

func replaceGroup(dst, src *xmltree.Node, name string) {
	var kept []*xmltree.Node
	for _, c := range dst.Children {
		if !(c.Kind == xmltree.Element && c.Name == name) {
			kept = append(kept, c)
		}
	}
	for _, c := range src.ChildrenNamed(name) {
		kept = append(kept, c.Clone())
	}
	dst.Children = kept
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/engine/ -v`
Expected: PASS for all `TestCommit*` and `TestClone`.

- [ ] **Step 6: Commit**

```bash
git add internal/engine internal/changes
git commit -m "engine: commit with policy, merge, lock retry, errors and write-back"
```

---
### Task 13: `engine.Pull`

Spec section 7.

Behaviour:

1. Compute local changes once (`changes.Compute`, no filter) and index them by identity: `localByID[id]` for files with an identity (status `M`, `R`, `!`, `C`, parse-failed `M`); `deletedLocally[id]` for `D`.
2. `Session.List(ctx, Index.Cursor)`. For each listed resource (skipped when a filter is set and neither the remote path nor the indexed path matches):
   - **Not in index**: if a working file already exists at its path → `  !  <path>   exists locally and is not tracked; skipped`. Else `Store` → `  +  <path>`.
   - **Stub with same version and path** as the index → nothing.
   - Resolve (fetch if stub). If canonical remote == canonical base and path unchanged → update index version only, nothing printed.
   - **Local file deleted** (pending `D`) → `  C  <path>   deleted locally, changed on remote` (base untouched; count conflict).
   - **Local conflicted** (`C`): `--force` → `Store(remote, localPath)` → `  ~  <path>   (forced)`; else `  C  <path>   unresolved conflict; resolve first`.
   - **Local unchanged** (not in `localByID`) → `Store(remote, entry.Path)` → `  ~  <path>` or `  ~  <old> -> <new>   (moved on remote)`.
   - **Local changed** → `merge.Merge(base, local, remote)`. Target path: the local path if the file was moved locally, else the remote path. Clean → write merged content (keeping the local envelope's `action`/params/errors) at the target, remove the old working file if the path changed, `StoreBase(remote at target path, entry.Path)` → `  ~  <path>   merged`. Conflicted → write conflict file at target, `StoreBase` likewise → `  C  <path>   conflict with remote v<N>`.
3. **Deleted on remote**: listed in `Listing.Deleted`, or (when `Listing.Full`) in the index but not listed. Local unchanged → `Forget` → `  -  <path>   (deleted on remote)`. Local changed → strip every read-only attribute (root and declared subs, recursively) from the local content, write it with `<conflict remote-version="deleted"/>`, drop base and index entry → `  C  <path>   deleted on remote; remove <conflict/> and commit to re-create, or delete the file`.
4. Save `Index.Cursor = Listing.Cursor`. If nothing was printed, print `Already up to date.`

**Files:**
- Create: `internal/engine/pull.go`
- Test: `internal/engine/pull_test.go`

**Interfaces:**
- Consumes: Tasks 11–12 (`Env`, `Store`, `StoreBase`, `Forget`, `writeConflict` pattern), `changes`, `merge`, `envelope`.
- Produces:
  - `type PullOpts struct{ Force bool; Filter func(string) bool }`
  - `type PullReport struct{ Added, Updated, Merged, Moved, Deleted, Conflicts int }`, `func (r PullReport) ExitCode() int` (1 if `Conflicts > 0`)
  - `func Pull(ctx context.Context, e *Env, o PullOpts) (PullReport, error)`
  - `func StripReadOnly(root *xmltree.Node, s *schema.Schema)` (exported for resolve)

- [ ] **Step 1: Write the failing test** `internal/engine/pull_test.go`

```go
package engine

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func pull(t *testing.T, env *Env, o PullOpts) PullReport {
	t.Helper()
	r, err := Pull(ctx, env, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setTitle(s string) func(*xmltree.Node) {
	return func(r *xmltree.Node) { r.Child("title").Children[0].Text = s }
}

func TestPullNewAndUpdated(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Put("9", "c/nine.xml", `<note><title>Nine</title></note>`)
	ad.Remote.Edit("1", setTitle("One v2"))
	r := pull(t, env, PullOpts{})
	if r.Added != 1 || r.Updated != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(read(t, env, "c/nine.xml"), "<title>Nine</title>") || !strings.Contains(read(t, env, "a/one.xml"), "One v2") {
		t.Fatal("files not written")
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status must be clean after pull")
	}
	out.Reset()
	pull(t, env, PullOpts{})
	if !strings.Contains(out.String(), "Already up to date.") {
		t.Fatal(out.String())
	}
}

func TestPullMergesIntoLocalWork(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("2", setTitle("Remote title"))
	edit(t, env, "a/b/two.xml", "line1", "LOCAL1")
	r := pull(t, env, PullOpts{})
	if r.Merged != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/b/two.xml")
	if !strings.Contains(got, "Remote title") || !strings.Contains(got, "LOCAL1") {
		t.Fatal(got)
	}
	if e, _ := env.Index.ByID("2"); e.Version != "2" {
		t.Fatal("base must be the remote version, not the merge")
	}
	if cs := status(t, env); len(cs) != 1 || cs[0].Status != 'M' {
		t.Fatalf("local edit must still be pending: %+v", cs)
	}
}

func TestPullConflictAndForce(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 || r.ExitCode() != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if !strings.Contains(read(t, env, "a/one.xml"), "<<<<<<< local") {
		t.Fatal("markers expected")
	}
	ad.Remote.Edit("1", setTitle("Remote again"))
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 {
		t.Fatalf("still conflicted: %+v", r)
	}
	pull(t, env, PullOpts{Force: true})
	if got := read(t, env, "a/one.xml"); strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "Remote again") {
		t.Fatal(got)
	}
}

func TestPullMovedAndDeleted(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Move("1", "z/one.xml")
	ad.Remote.Delete("2")
	r := pull(t, env, PullOpts{})
	if r.Moved != 1 || r.Deleted != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	if env.Tree.Exists("a/one.xml") || !env.Tree.Exists("z/one.xml") || env.Tree.Exists("a/b/two.xml") {
		t.Fatal("moves/deletes not applied")
	}
	if len(status(t, env)) != 0 {
		t.Fatal("status must be clean")
	}
}

func TestPullDeletedRemotelyWithLocalEdits(t *testing.T) {
	env, ad, out := cloned(t)
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Keep me</title>")
	ad.Remote.Delete("1")
	if r := pull(t, env, PullOpts{}); r.Conflicts != 1 {
		t.Fatalf("%+v\n%s", r, out)
	}
	got := read(t, env, "a/one.xml")
	if !strings.Contains(got, `<conflict remote-version="deleted"`) || strings.Contains(got, `id="`) || !strings.Contains(got, "Keep me") {
		t.Fatal(got)
	}
	// re-create: remove the conflict element and commit
	edit(t, env, "a/one.xml", `  <conflict remote-version="deleted" elements="0" hunks="0"/>`+"\n", "")
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 {
		t.Fatalf("re-create: %+v\n%s", r, out)
	}
}

func TestPullNeverTouchesUntracked(t *testing.T) {
	env, ad, out := cloned(t)
	write(t, env, "c/nine.xml", "<note><title>mine</title></note>")
	ad.Remote.Put("9", "c/nine.xml", `<note><title>theirs</title></note>`)
	pull(t, env, PullOpts{})
	if !strings.Contains(read(t, env, "c/nine.xml"), "mine") || !strings.Contains(out.String(), "not tracked") {
		t.Fatalf("%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run Pull`
Expected: FAIL, undefined `Pull`.

- [ ] **Step 3: Implement** `internal/engine/pull.go`

```go
package engine

import (
	"context"
	"fmt"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/merge"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type PullOpts struct {
	Force  bool
	Filter func(string) bool
}

type PullReport struct{ Added, Updated, Merged, Moved, Deleted, Conflicts int }

func (r PullReport) ExitCode() int {
	if r.Conflicts > 0 {
		return 1
	}
	return 0
}

func Pull(ctx context.Context, e *Env, o PullOpts) (PullReport, error) {
	var r PullReport
	s := e.Adapter.Schema()
	cs, err := changes.Compute(e.Tree, e.Index, e.Adapter, nil)
	if err != nil {
		return r, err
	}
	localByID := map[string]changes.FileChange{}
	deletedLocally := map[string]bool{}
	for _, c := range cs {
		switch {
		case c.Status == 'D':
			deletedLocally[c.ID] = true
		case c.ID != "":
			localByID[c.ID] = c
		}
	}
	l, err := e.Session.List(ctx, e.Index.Cursor)
	if err != nil {
		return r, err
	}
	printed := false
	say := func(format string, a ...any) {
		printed = true
		fmt.Fprintf(e.Out, format+"\n", a...)
	}
	listed := map[string]bool{}
	for _, item := range l.Resources {
		listed[item.ID] = true
		entry, known := e.Index.ByID(item.ID)
		if o.Filter != nil && !o.Filter(item.Path) && !(known && o.Filter(entry.Path)) {
			continue
		}
		if !known {
			if e.Tree.Exists(item.Path) {
				say("  !  %s   exists locally and is not tracked; skipped", item.Path)
				continue
			}
			res, err := e.Resolved(ctx, item)
			if err != nil {
				return r, err
			}
			if err := e.Store(res, ""); err != nil {
				return r, err
			}
			r.Added++
			say("  +  %s", res.Path)
			continue
		}
		if item.Root == nil && item.Version == entry.Version && item.Path == entry.Path {
			continue
		}
		res, err := e.Resolved(ctx, item)
		if err != nil {
			return r, err
		}
		bdata, err := e.Tree.ReadBase(entry.Path)
		if err != nil {
			return r, err
		}
		base, err := envelope.Parse(bdata)
		if err != nil {
			return r, fmt.Errorf("base %s: %w", entry.Path, err)
		}
		if res.Path == entry.Path && changes.CanonContent(res.Root, s) == changes.CanonContent(base.Content, s) {
			if res.Version != entry.Version {
				entry.Version = res.Version
				e.Index.Put(entry)
			}
			continue
		}
		local, changed := localByID[item.ID]
		switch {
		case deletedLocally[item.ID]:
			r.Conflicts++
			say("  C  %s   deleted locally, changed on remote", entry.Path)
		case changed && local.Status == 'C':
			if !o.Force {
				r.Conflicts++
				say("  C  %s   unresolved conflict; resolve first", local.Path)
				continue
			}
			if err := e.Store(res, local.Path); err != nil {
				return r, err
			}
			r.Updated++
			say("  ~  %s   (forced)", res.Path)
		case !changed:
			if err := e.Store(res, entry.Path); err != nil {
				return r, err
			}
			if res.Path != entry.Path {
				r.Moved++
				say("  ~  %s -> %s   (moved on remote)", entry.Path, res.Path)
			} else {
				r.Updated++
				say("  ~  %s", res.Path)
			}
		default:
			if local.Local == nil { // unparseable local file: cannot merge
				r.Conflicts++
				say("  C  %s   local file does not parse: %v", local.Path, local.Err)
				continue
			}
			target := res.Path
			if local.Status == 'R' {
				target = local.Path
			}
			m, err := merge.Merge(base.Content, local.Local.Content, res.Root, s, "remote v"+res.Version)
			if err != nil {
				r.Conflicts++
				say("  C  %s   merge failed: %v", local.Path, err)
				continue
			}
			tb := *res
			tb.Path = target
			if m.Conflicted() {
				doc := &envelope.Doc{Action: local.Local.Action, Params: local.Local.Params, Conflict: &envelope.Conflict{
					RemoteVersion: res.Version, By: res.By, At: res.At, Elements: m.Elements, Hunks: m.Hunks}}
				if err := e.Tree.WriteFile(target, []byte(envelope.Header(doc)+m.Text+"\n"+envelope.Footer)); err != nil {
					return r, err
				}
				r.Conflicts++
				say("  C  %s   conflict with remote v%s", target, res.Version)
			} else {
				doc := *local.Local
				doc.Content = m.Root
				if err := e.Tree.WriteFile(target, envelope.Bytes(&doc, s)); err != nil {
					return r, err
				}
				r.Merged++
				say("  ~  %s   merged", target)
			}
			if target != local.Path && e.Tree.Exists(local.Path) {
				if err := e.Tree.Remove(local.Path); err != nil {
					return r, err
				}
			}
			if err := e.StoreBase(&tb, entry.Path); err != nil {
				return r, err
			}
		}
	}
	gone := map[string]bool{}
	for _, id := range l.Deleted {
		gone[id] = true
	}
	if l.Full {
		for _, en := range e.Index.All() {
			if !listed[en.ID] {
				gone[en.ID] = true
			}
		}
	}
	for id := range gone {
		en, ok := e.Index.ByID(id)
		if !ok || (o.Filter != nil && !o.Filter(en.Path)) {
			continue
		}
		local, changed := localByID[id]
		if !changed || local.Local == nil {
			if err := e.Forget(id, en.Path); err != nil {
				return r, err
			}
			r.Deleted++
			say("  -  %s   (deleted on remote)", en.Path)
			continue
		}
		doc := *local.Local
		doc.Content = local.Local.Content.Clone()
		StripReadOnly(doc.Content, s)
		doc.Conflict = &envelope.Conflict{RemoteVersion: "deleted"}
		if err := e.Tree.WriteFile(local.Path, envelope.Bytes(&doc, s)); err != nil {
			return r, err
		}
		if err := e.Tree.RemoveBase(en.Path); err != nil {
			return r, err
		}
		e.Index.Delete(id)
		r.Conflicts++
		say("  C  %s   deleted on remote; remove <conflict/> and commit to re-create, or delete the file", local.Path)
	}
	e.Index.Cursor = l.Cursor
	if err := e.Tree.SaveIndex(e.Index); err != nil {
		return r, err
	}
	if !printed {
		fmt.Fprintln(e.Out, "Already up to date.")
	}
	return r, nil
}

// StripReadOnly removes service-owned attributes so the content can be re-created.
func StripReadOnly(root *xmltree.Node, s *schema.Schema) {
	for _, a := range s.RootAttrs {
		if a.ReadOnly {
			root.DelAttr(a.Name)
		}
	}
	stripSubs(root, s.Elems)
}

func stripSubs(n *xmltree.Node, elems []schema.Elem) {
	for _, c := range n.Elements() {
		e := schema.Find(elems, c.Name)
		if e == nil || e.Kind != schema.Sub {
			continue
		}
		for _, a := range e.Attrs {
			if a.ReadOnly || a.Name == e.ID {
				c.DelAttr(a.Name)
			}
		}
		stripSubs(c, e.Children)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/engine/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "engine: pull with merge, moves, remote deletes and --force"
```

---
### Task 14: `engine.Resolve`

Spec section 3.6. Nothing is sent.

Behaviour per path (relative to the tree):

- Not conflicted (no markers, no envelope `<conflict>`) → error `<path>: not in conflict`.
- `<conflict remote-version="deleted">`: `--theirs` → remove the working file; `--ours` → remove the `<conflict>` element (the file becomes a create).
- Markers present: walk lines; outside a conflict keep the line; inside keep only the chosen section (`--ours`: between `<<<<<<< ` and `||||||| `; `--theirs`: between `=======` and `>>>>>>> `). Parse the result (`envelope.Parse`), clear `Conflict`, write canonical. A parse failure is an error telling the user to edit by hand.
- `<conflict>` without markers (from `--no-merge`): `--ours` → clear `Conflict`; `--theirs` → `Session.Fetch(id)` and `Store` the remote (this also moves base to the remote, which `--no-merge` had not done).
- Print `resolved <path> (ours|theirs)` per file.

**Files:**
- Create: `internal/engine/resolve.go`
- Test: `internal/engine/resolve_test.go`

**Interfaces:**
- Produces: `func Resolve(ctx context.Context, e *Env, paths []string, ours bool) error`

- [ ] **Step 1: Write the failing test** `internal/engine/resolve_test.go`

```go
package engine

import (
	"strings"
	"testing"
)

func TestResolveOursThenCommit(t *testing.T) {
	env, ad, out := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, true); err != nil {
		t.Fatal(err)
	}
	got := read(t, env, "a/one.xml")
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, "<conflict") || !strings.Contains(got, "<title>Local</title>") {
		t.Fatal(got)
	}
	out.Reset()
	if r := commit(t, env, CommitOpts{}); r.ExitCode() != 0 || title(t, env, "1") != "Local" {
		t.Fatalf("%+v\n%s", r, out)
	}
}

func TestResolveTheirs(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if len(status(t, env)) != 0 {
		t.Fatalf("theirs must leave a clean tree: %+v", status(t, env))
	}
}

func TestResolveNoMergeTheirs(t *testing.T) {
	env, ad, _ := cloned(t)
	ad.Remote.Edit("1", setTitle("Remote"))
	edit(t, env, "a/one.xml", "<title>One</title>", "<title>Local</title>")
	commit(t, env, CommitOpts{NoMerge: true})
	if err := Resolve(ctx, env, []string{"a/one.xml"}, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, env, "a/one.xml"); !strings.Contains(got, "<title>Remote</title>") || len(status(t, env)) != 0 {
		t.Fatal(got)
	}
}

func TestResolveNotConflicted(t *testing.T) {
	env, _, _ := cloned(t)
	if err := Resolve(ctx, env, []string{"a/one.xml"}, true); err == nil || !strings.Contains(err.Error(), "not in conflict") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/engine/ -run Resolve`
Expected: FAIL, undefined `Resolve`.

- [ ] **Step 3: Implement** `internal/engine/resolve.go`

```go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
)

func Resolve(ctx context.Context, e *Env, paths []string, ours bool) error {
	side := "theirs"
	if ours {
		side = "ours"
	}
	s := e.Adapter.Schema()
	for _, p := range paths {
		data, err := e.Tree.ReadFile(p)
		if err != nil {
			return err
		}
		markers := envelope.HasMarkers(data)
		if !markers && !envelope.HasConflictElement(data) {
			return fmt.Errorf("%s: not in conflict", p)
		}
		if markers {
			data = []byte(pickSide(string(data), ours))
		}
		doc, err := envelope.Parse(data)
		if err != nil {
			return fmt.Errorf("%s: result does not parse (%v); edit the file by hand", p, err)
		}
		switch {
		case doc.Conflict != nil && doc.Conflict.RemoteVersion == "deleted" && !ours:
			if err := e.Tree.Remove(p); err != nil {
				return err
			}
		case !markers && !ours:
			en, ok := e.Index.ByPath(p)
			if !ok {
				return fmt.Errorf("%s: not tracked", p)
			}
			remote, err := e.Session.Fetch(ctx, en.ID)
			if err != nil {
				return err
			}
			if err := e.Store(remote, p); err != nil {
				return err
			}
		default:
			doc.Conflict = nil
			if err := e.Tree.WriteFile(p, envelope.Bytes(doc, s)); err != nil {
				return err
			}
		}
		fmt.Fprintf(e.Out, "resolved %s (%s)\n", p, side)
	}
	return nil
}

func pickSide(text string, ours bool) string {
	const (
		outside = iota
		local
		base
		remote
	)
	state := outside
	var out []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			state = local
			continue
		case strings.HasPrefix(line, "||||||| ") && state == local:
			state = base
			continue
		case line == "=======" && (state == base || state == local):
			state = remote
			continue
		case strings.HasPrefix(line, ">>>>>>> ") && state == remote:
			state = outside
			continue
		}
		if state == outside || (ours && state == local) || (!ours && state == remote) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/engine/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "engine: resolve --ours/--theirs"
```

---
### Task 15: CLI commands

Spec section 3. Thin cobra wrappers over the engine. One file per command.

Output formats:

- `status`: first line `On remote <url>`. One line per change: `  <letter>  <path or old -> new>   <detail>  [ask]|[deny]`, where for a single action the detail is the action's `Detail`; for several actions the file line has no detail and each action is printed on its own line indented by 8 spaces. `!` lines show `failed: <first error msg>`; `C` lines `unresolved conflict`; invalid files `invalid: <err>`; notes (flat rename) are appended as `(<note>)`. No changes → `nothing to commit, working tree matches the remote`. Paths are printed relative to the tree root.
- `diff`: for each change, the status line, then `textdiff.Unified("a/<base path>", "b/<path>", baseLines, workLines)` over canonical text (working files that do not parse are diffed raw).
- `commit`, `pull`: engine output; non-zero report → exit 1.
- `log`: `LogEntry.String()` per line, newest last; `-n` keeps the last n; path args filter by `Path` or `NewPath`.
- `actions`: implicit verbs `create`, `update`, `delete`, `move` first, then adapter verbs; one line each: `<name>  <policy level>  <params, comma-separated or ->  <help>`.

Common setup (`openEnv`): find the tree from the cwd, load config, `adapter.ForURL(config remote.url)`, `Open(ctx, u, config.Section("remote"))`, load index, take the lock (released by the returned `close` func, which also closes the session). `Prompt` is set only when both stdin and stdout are terminals (`golang.org/x/term`); it prints `<question> [y/N] ` and accepts `y`/`yes`.

**Files:**
- Create: `internal/cli/env.go`, `internal/cli/clone.go`, `internal/cli/status.go`, `internal/cli/diff.go`, `internal/cli/commit.go`, `internal/cli/pull.go`, `internal/cli/resolve.go`, `internal/cli/log.go`, `internal/cli/actions.go`
- Modify: `internal/cli/root.go` (register subcommands)
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `engine` (Tasks 11–14), `changes`, `policy`, `workdir`, `adapter`, `textdiff`, `envelope`.
- Produces: `gfs clone|status|diff|commit|pull|resolve|log|actions`.

- [ ] **Step 1: Write the failing test** `internal/cli/cli_test.go`

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/fake"
)

var fk = fake.New()

func init() {
	adapter.Register(fk) // test-only registration
	fk.Remote.Put("1", "a/one.xml", `<note><title>One</title></note>`)
}

func gfs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String(), exitCode(err)
}

func TestCLIFlow(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if out, code := gfs(t, "clone", "fake://x", "wt"); code != 0 {
		t.Fatal(out)
	}
	t.Chdir(filepath.Join(dir, "wt", "a"))
	if out, _ := gfs(t, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}
	data, _ := os.ReadFile("one.xml")
	os.WriteFile("one.xml", bytes.Replace(data, []byte("<title>One</title>"), []byte("<title>Uno</title>"), 1), 0o644)

	out, _ := gfs(t, "status")
	if !strings.Contains(out, "On remote fake://x") || !strings.Contains(out, "  M  a/one.xml") || !strings.Contains(out, "update title") {
		t.Fatal(out)
	}
	out, _ = gfs(t, "diff", "one.xml")
	if !strings.Contains(out, "-      <title>One</title>") || !strings.Contains(out, "+      <title>Uno</title>") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit", "--dry-run"); code != 0 || !strings.Contains(out, "would run") {
		t.Fatal(out)
	}
	if out, code := gfs(t, "commit"); code != 0 || !strings.Contains(out, "update  a/one.xml   ok") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "log"); !strings.Contains(out, "update  a/one.xml  ok") {
		t.Fatal(out)
	}

	os.Remove("one.xml")
	if out, code := gfs(t, "commit"); code != 1 || !strings.Contains(out, "--allow delete") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if out, code := gfs(t, "commit", "--allow", "delete"); code != 0 {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "actions"); !strings.Contains(out, "publish  ask  channel") {
		t.Fatal(out)
	}
	if out, _ := gfs(t, "pull"); !strings.Contains(out, "Already up to date.") {
		t.Fatal(out)
	}
	if _, code := gfs(t, "resolve", "x.xml"); code != 2 {
		t.Fatal("resolve without --ours/--theirs must be a usage error")
	}
}

func TestOutsideTree(t *testing.T) {
	t.Chdir(t.TempDir())
	if out, code := gfs(t, "status"); code != 2 || !strings.Contains(out, ".gfs not found") {
		t.Fatalf("%d %s", code, out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli/`
Expected: FAIL, `unknown command "clone"`.

- [ ] **Step 3: Implement**

`internal/cli/env.go`:

```go
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func usage(format string, a ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format, a...)}
}

func prompter() func(string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}
	in := bufio.NewReader(os.Stdin)
	return func(q string) bool {
		fmt.Fprintf(os.Stdout, "%s [y/N] ", q)
		ans, _ := in.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes"
	}
}

// openEnv opens the working tree containing the cwd. Call the returned func when done.
func openEnv(cmd *cobra.Command, lock bool) (*engine.Env, string, func(), error) {
	t, err := workdir.Find(".")
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := t.LoadConfig()
	if err != nil {
		return nil, "", nil, err
	}
	raw := cfg.Get("remote", "url")
	ad, u, err := adapter.ForURL(raw)
	if err != nil {
		return nil, "", nil, err
	}
	ix, err := t.LoadIndex()
	if err != nil {
		return nil, "", nil, err
	}
	unlock := func() {}
	if lock {
		if unlock, err = t.Lock(); err != nil {
			return nil, "", nil, err
		}
	}
	sess, err := ad.Open(context.Background(), u, cfg.Section("remote"))
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	env := &engine.Env{Tree: t, Index: ix, Adapter: ad, Session: sess, Out: cmd.OutOrStdout(), Prompt: prompter(), Now: time.Now}
	return env, raw, func() { sess.Close(); unlock() }, nil
}

func exitFor(code int) error {
	if code == 0 {
		return nil
	}
	return &ExitError{Code: code}
}
```

`internal/cli/clone.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newClone() *cobra.Command {
	return &cobra.Command{
		Use:   "clone <url> [<dir>]",
		Short: "Create a working tree from a remote",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ad, u, err := adapter.ForURL(args[0])
			if err != nil {
				return usage("%v", err)
			}
			dir := ad.DefaultDir(u)
			if len(args) == 2 {
				dir = args[1]
			}
			sess, err := ad.Open(cmd.Context(), u, nil)
			if err != nil {
				return err
			}
			defer sess.Close()
			_, err = engine.Clone(cmd.Context(), ad, sess, args[0], dir, cmd.OutOrStdout())
			return err
		},
	}
}
```

`internal/cli/status.go`:

```go
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
)

func newStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status [<path>...]",
		Short: "Show changed files and the remote actions they resolve to",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, url, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cs, pol, err := computeChanges(env, args)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "On remote %s\n", url)
			if len(cs) == 0 {
				fmt.Fprintln(w, "nothing to commit, working tree matches the remote")
			}
			for _, c := range cs {
				printChange(w, c, pol)
			}
			return nil
		},
	}
}

func computeChanges(env *engine.Env, args []string) ([]changes.FileChange, *policy.Policy, error) {
	filter, err := changes.PathFilter(env.Tree, args)
	if err != nil {
		return nil, nil, usage("%v", err)
	}
	cfg, err := env.Tree.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	pol, err := policy.FromConfig(cfg.Section("policy"))
	if err != nil {
		return nil, nil, err
	}
	cs, err := changes.Compute(env.Tree, env.Index, env.Adapter, filter)
	return cs, pol, err
}

func mark(pol *policy.Policy, a adapter.Action) string {
	switch pol.Level(a.Class) {
	case policy.Ask:
		return "  [ask]"
	case policy.Deny:
		return "  [deny]"
	}
	return ""
}

func printChange(w io.Writer, c changes.FileChange, pol *policy.Policy) {
	p := c.Path
	if c.OldPath != "" {
		p = c.OldPath + " -> " + c.Path
	}
	detail := ""
	switch {
	case c.Status == 'C':
		detail = "unresolved conflict"
	case c.Err != nil:
		detail = "invalid: " + strings.ReplaceAll(c.Err.Error(), "\n", "; ")
	case c.Status == '!' && c.Local != nil && len(c.Local.Errors) > 0:
		detail = "failed: " + c.Local.Errors[0].Msg
	case len(c.Actions) == 1:
		detail = c.Actions[0].Detail + mark(pol, c.Actions[0])
	}
	if c.Note != "" {
		detail = strings.TrimSpace(detail + "  (" + c.Note + ")")
	}
	fmt.Fprintf(w, "  %c  %-40s %s\n", c.Status, p, detail)
	if len(c.Actions) > 1 && c.Err == nil {
		for _, a := range c.Actions {
			fmt.Fprintf(w, "        %s%s\n", a.Detail, mark(pol, a))
		}
	}
}
```

`internal/cli/diff.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/envelope"
	"github.com/KrzysztofBogdan/gitfs/internal/textdiff"
)

func newDiff() *cobra.Command {
	return &cobra.Command{
		Use:   "diff [<path>...]",
		Short: "Show resolved actions and the canonical diff against base",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cs, pol, err := computeChanges(env, args)
			if err != nil {
				return err
			}
			s := env.Adapter.Schema()
			w := cmd.OutOrStdout()
			for _, c := range cs {
				printChange(w, c, pol)
				var before, after string
				if c.Base != nil {
					before = string(envelope.Bytes(c.Base, s))
				}
				switch {
				case c.Status == 'D':
				case c.Local != nil:
					after = string(envelope.Bytes(c.Local, s))
				default:
					raw, _ := env.Tree.ReadFile(c.Path)
					after = string(raw)
				}
				basePath := c.Path
				if c.OldPath != "" {
					basePath = c.OldPath
				}
				fmt.Fprint(w, textdiff.Unified("a/"+basePath, "b/"+c.Path, textdiff.Lines(before), textdiff.Lines(after)))
			}
			return nil
		},
	}
}
```

`internal/cli/commit.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newCommit() *cobra.Command {
	var o engine.CommitOpts
	var allow []string
	cmd := &cobra.Command{
		Use:   "commit [<path>...]",
		Short: "Make the remote look like the working tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			if o.Filter, err = changes.PathFilter(env.Tree, args); err != nil {
				return usage("%v", err)
			}
			o.Allow = map[string]bool{}
			for _, a := range allow {
				o.Allow[a] = true
			}
			r, err := engine.Commit(cmd.Context(), env, o)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "resolve and check everything, execute nothing")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "treat ask as allow for this policy class (repeatable)")
	cmd.Flags().BoolVar(&o.NoMerge, "no-merge", false, "refuse files whose remote moved instead of merging")
	return cmd
}
```

`internal/cli/pull.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newPull() *cobra.Command {
	var o engine.PullOpts
	cmd := &cobra.Command{
		Use:   "pull [<path>...]",
		Short: "Bring the working tree up to date with the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			if o.Filter, err = changes.PathFilter(env.Tree, args); err != nil {
				return usage("%v", err)
			}
			r, err := engine.Pull(cmd.Context(), env, o)
			if err != nil {
				return err
			}
			return exitFor(r.ExitCode())
		},
	}
	cmd.Flags().BoolVar(&o.Force, "force", false, "replace conflicted files with the remote version")
	return cmd
}
```

`internal/cli/resolve.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/engine"
)

func newResolve() *cobra.Command {
	var ours, theirs bool
	cmd := &cobra.Command{
		Use:   "resolve (--ours | --theirs) <path>...",
		Short: "Resolve conflicted files by picking one side",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ours == theirs {
				return usage("exactly one of --ours or --theirs is required")
			}
			env, _, done, err := openEnv(cmd, true)
			if err != nil {
				return err
			}
			defer done()
			var rels []string
			for _, a := range args {
				r, err := env.Tree.Rel(a)
				if err != nil {
					return usage("%v", err)
				}
				rels = append(rels, r)
			}
			return engine.Resolve(cmd.Context(), env, rels, ours)
		},
	}
	cmd.Flags().BoolVar(&ours, "ours", false, "keep the local side")
	cmd.Flags().BoolVar(&theirs, "theirs", false, "keep the remote side")
	return cmd
}
```

`internal/cli/log.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/workdir"
)

func newLog() *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log [-n <count>] [<path>...]",
		Short: "Show what commits did on the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := workdir.Find(".")
			if err != nil {
				return err
			}
			filter, err := changes.PathFilter(t, args)
			if err != nil {
				return usage("%v", err)
			}
			es, err := t.ReadLog()
			if err != nil {
				return err
			}
			var kept []workdir.LogEntry
			for _, e := range es {
				if filter == nil || filter(e.Path) || (e.NewPath != "" && filter(e.NewPath)) {
					kept = append(kept, e)
				}
			}
			if n > 0 && len(kept) > n {
				kept = kept[len(kept)-n:]
			}
			for _, e := range kept {
				fmt.Fprintln(cmd.OutOrStdout(), e.String())
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&n, "count", "n", 0, "show only the last n entries")
	return cmd
}
```

`internal/cli/actions.go`:

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/policy"
)

func newActions() *cobra.Command {
	return &cobra.Command{
		Use:   "actions",
		Short: "List the verbs this remote understands and their policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, done, err := openEnv(cmd, false)
			if err != nil {
				return err
			}
			defer done()
			cfg, err := env.Tree.LoadConfig()
			if err != nil {
				return err
			}
			pol, err := policy.FromConfig(cfg.Section("policy"))
			if err != nil {
				return err
			}
			verbs := []adapter.Verb{
				{Name: "create", Class: "create", Help: "new file"},
				{Name: "update", Class: "update", Help: "changed element or sub-resource"},
				{Name: "delete", Class: "delete", Help: "removed file or sub-resource"},
				{Name: "move", Class: "move", Help: "moved or renamed file"},
			}
			verbs = append(verbs, env.Adapter.Verbs()...)
			for _, v := range verbs {
				params := "-"
				if len(v.Params) > 0 {
					params = strings.Join(v.Params, ",")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", v.Name, pol.Level(v.Class), params, v.Help)
			}
			return nil
		},
	}
}
```

In `internal/cli/root.go`, inside `NewRoot` before `return root`:

```go
	root.AddCommand(newClone(), newStatus(), newDiff(), newCommit(), newPull(), newResolve(), newLog(), newActions())
```

Run `go get golang.org/x/term` if `go mod tidy` does not add it.

- [ ] **Step 4: Run tests**

Run: `go mod tidy && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli go.mod go.sum
git commit -m "cli: clone, status, diff, commit, pull, resolve, log, actions"
```

---
## Confluence connector

Target: **Confluence Cloud REST API v2** (`/wiki/api/v2/...`), plus the two v1 label endpoints that v2 lacks. Remote URL: `confluence://<host>/<SPACEKEY>` (e.g. `confluence://acme.atlassian.net/ENG`). Working tree layout (spec 10.1, `example/confluence/`): `<spacekey lowercased>/<Title>.xml`, children in `<Title>/`. Path model `Tree`: moving a file changes the parent, renaming it renames the page.

Endpoints used (all JSON, Basic auth `email:api-token`):

| purpose | request |
|---|---|
| space by key | `GET /wiki/api/v2/spaces?keys=<KEY>` → `results[0].{id,key,homepageId}` |
| pages in space (tree) | `GET /wiki/api/v2/spaces/<spaceId>/pages?limit=250` → `{id,title,parentId,version.number}`; follow `_links.next` |
| page | `GET /wiki/api/v2/pages/<id>?body-format=storage` |
| labels | `GET /wiki/api/v2/pages/<id>/labels?limit=250` (keep `prefix == "global"`) |
| footer comments | `GET /wiki/api/v2/pages/<id>/footer-comments?body-format=storage&limit=250` |
| user name | `GET /wiki/rest/api/user?accountId=<id>` → `displayName` |
| create page | `POST /wiki/api/v2/pages` `{spaceId,status:"current",title,parentId?,body:{representation:"storage",value}}` |
| update page (title, body, parent) | `PUT /wiki/api/v2/pages/<id>` `{id,status:"current",title,parentId?,body,version:{number:n+1,message:"gfs"}}`; **409 = version lock failure** |
| delete page | `DELETE /wiki/api/v2/pages/<id>` |
| add label | `POST /wiki/rest/api/content/<id>/label` `[{"prefix":"global","name":"x"}]` |
| remove label | `DELETE /wiki/rest/api/content/<id>/label?name=x` |
| comment create / update / delete | `POST /wiki/api/v2/footer-comments` `{pageId,body}`; `PUT /wiki/api/v2/footer-comments/<id>` `{version:{number:n+1},body}`; `DELETE /wiki/api/v2/footer-comments/<id>` |

Credentials: `GFS_CONFLUENCE_TOKEN` (required, environment only, never written to `.gfs/config`); email from `GFS_CONFLUENCE_EMAIL`, else the URL user part, else `[remote] email`. `[remote] base` (or `?base=` in the URL) overrides `https://<host>`; tests use it to point at the fake server.

### Task 16: Confluence fake server, target parsing, REST client

**Files:**
- Create: `internal/adapter/confluence/cftest/server.go`, `internal/adapter/confluence/url.go`, `internal/adapter/confluence/client.go`
- Test: `internal/adapter/confluence/client_test.go`

**Interfaces:**
- Produces (package `cftest`):
  - `type Page struct{ ID, Title, ParentID, SpaceID, Storage, AuthorID, CreatedAt, UpdatedAt string; Version int; Labels []string }`
  - `type Comment struct{ ID, PageID, Storage, AuthorID, CreatedAt string; Version int }`
  - `type Server struct{ *httptest.Server; PageLimit int; Requests []string; ... }` — `Requests` holds `"METHOD /path"` per request
  - `func New() *Server` (users `me` → `Me`, `bob` → `bob`; clock fixed at `2026-09-23T12:00:00.000Z`)
  - `func (s *Server) AddSpace(key, id string)`, `AddPage(p Page) *Page` (fills defaults: `Version` 1, `AuthorID` `me`, timestamps), `AddComment(c Comment) *Comment`, `EditPage(id string, f func(*Page))` (bumps version, author `bob`), `Page(id string) (*Page, bool)`, `DeletePage(id string)`, `Comments(pageID string) []*Comment`
- Produces (package `confluence`):
  - `type target struct{ base, host, space, email, token string }`; `func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string) (target, error)`
  - `type APIError struct{ Status int; Body string }`
  - `type client struct{ ... }`; `func newClient(t target) *client`; `func (c *client) do(ctx context.Context, method, path string, in, out any) error` (404 wraps `adapter.ErrNotFound`, 409 wraps `adapter.ErrLock`, both also wrap the `*APIError`); `func (c *client) paginate(ctx context.Context, path string, each func(json.RawMessage) error) error`; `func codeOf(err error) string` (HTTP status as text or `""`)

- [ ] **Step 1: Write the failing test** `internal/adapter/confluence/client_test.go`

```go
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestParseTarget(t *testing.T) {
	creds := env(map[string]string{"GFS_CONFLUENCE_TOKEN": "tok", "GFS_CONFLUENCE_EMAIL": "me@x.com"})
	cases := []struct {
		raw     string
		cfg     map[string]string
		getenv  func(string) string
		base    string
		space   string
		email   string
		wantErr string
	}{
		{"confluence://acme.atlassian.net/ENG", nil, creds, "https://acme.atlassian.net", "ENG", "me@x.com", ""},
		{"confluence://acme.atlassian.net/ENG?base=http://127.0.0.1:9", nil, creds, "http://127.0.0.1:9", "ENG", "me@x.com", ""},
		{"confluence://u%40x.com@acme.atlassian.net/ENG", map[string]string{"base": "http://b"}, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), "http://b", "ENG", "u@x.com", ""},
		{"confluence://acme.atlassian.net/", nil, creds, "", "", "", "want confluence://<host>/<SPACEKEY>"},
		{"confluence://acme.atlassian.net/ENG", nil, env(nil), "", "", "", "GFS_CONFLUENCE_TOKEN"},
		{"confluence://acme.atlassian.net/ENG", nil, env(map[string]string{"GFS_CONFLUENCE_TOKEN": "t"}), "", "", "", "GFS_CONFLUENCE_EMAIL"},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		got, err := parseTarget(u, c.cfg, c.getenv)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.raw, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.base != c.base || got.space != c.space || got.email != c.email {
			t.Errorf("%s: got %+v %v", c.raw, got, err)
		}
	}
}

func testClient(s *cftest.Server) *client {
	return newClient(target{base: s.URL, space: "ENG", email: "me@x.com", token: "t"})
}

func TestPaginate(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.PageLimit = 2
	s.AddSpace("ENG", "100")
	for _, title := range []string{"A", "B", "C", "D", "E"} {
		s.AddPage(cftest.Page{Title: title, SpaceID: "100"})
	}
	var titles []string
	err := testClient(s).paginate(context.Background(), "/wiki/api/v2/spaces/100/pages?limit=250", func(raw json.RawMessage) error {
		var p struct{ Title string }
		json.Unmarshal(raw, &p)
		titles = append(titles, p.Title)
		return nil
	})
	if err != nil || strings.Join(titles, "") != "ABCDE" {
		t.Fatalf("%v %v", titles, err)
	}
}

func TestErrorMapping(t *testing.T) {
	s := cftest.New()
	defer s.Close()
	s.AddSpace("ENG", "100")
	p := s.AddPage(cftest.Page{Title: "A", SpaceID: "100", Storage: "<p>x</p>"})
	c := testClient(s)
	ctx := context.Background()

	err := c.do(ctx, "GET", "/wiki/api/v2/pages/999?body-format=storage", nil, nil)
	if !errors.Is(err, adapter.ErrNotFound) || codeOf(err) != "404" {
		t.Fatalf("404: %v", err)
	}
	stale := map[string]any{"id": p.ID, "status": "current", "title": "A",
		"body": map[string]any{"representation": "storage", "value": "<p>y</p>"}, "version": map[string]any{"number": 1}}
	err = c.do(ctx, "PUT", "/wiki/api/v2/pages/"+p.ID, stale, nil)
	if !errors.Is(err, adapter.ErrLock) || codeOf(err) != "409" {
		t.Fatalf("409: %v", err)
	}
	anon := newClient(target{base: s.URL})
	if err := anon.do(ctx, "GET", "/wiki/api/v2/spaces?keys=ENG", nil, nil); codeOf(err) != "401" {
		t.Fatalf("401: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/...`
Expected: FAIL, packages/functions undefined.

- [ ] **Step 3: Implement** `internal/adapter/confluence/cftest/server.go`

```go
// Package cftest is an in-memory Confluence Cloud (REST v2 subset) for tests.
package cftest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
)

type Page struct {
	ID, Title, ParentID, SpaceID, Storage, AuthorID, CreatedAt, UpdatedAt string
	Version                                                             int
	Labels                                                              []string
}

type Comment struct {
	ID, PageID, Storage, AuthorID, CreatedAt string
	Version                                  int
}

type Server struct {
	*httptest.Server
	PageLimit int
	Requests  []string

	mu       sync.Mutex
	spaces   map[string]string // key -> id
	pages    map[string]*Page
	comments map[string]*Comment
	users    map[string]string
	seq      int
	now      string
}

func New() *Server {
	s := &Server{PageLimit: 250, spaces: map[string]string{}, pages: map[string]*Page{},
		comments: map[string]*Comment{}, users: map[string]string{"me": "Me", "bob": "bob"},
		seq: 1000, now: "2026-09-23T12:00:00.000Z"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wiki/api/v2/spaces", s.getSpaces)
	mux.HandleFunc("GET /wiki/api/v2/spaces/{id}/pages", s.listPages)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}", s.getPage)
	mux.HandleFunc("POST /wiki/api/v2/pages", s.createPage)
	mux.HandleFunc("PUT /wiki/api/v2/pages/{id}", s.updatePage)
	mux.HandleFunc("DELETE /wiki/api/v2/pages/{id}", s.deletePage)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/labels", s.getLabels)
	mux.HandleFunc("GET /wiki/api/v2/pages/{id}/footer-comments", s.getComments)
	mux.HandleFunc("POST /wiki/api/v2/footer-comments", s.createComment)
	mux.HandleFunc("PUT /wiki/api/v2/footer-comments/{id}", s.updateComment)
	mux.HandleFunc("DELETE /wiki/api/v2/footer-comments/{id}", s.deleteComment)
	mux.HandleFunc("POST /wiki/rest/api/content/{id}/label", s.addLabel)
	mux.HandleFunc("DELETE /wiki/rest/api/content/{id}/label", s.removeLabel)
	mux.HandleFunc("GET /wiki/rest/api/user", s.getUser)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
		if u, p, ok := r.BasicAuth(); !ok || u == "" || p == "" {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return s
}

func (s *Server) next() string { s.seq++; return strconv.Itoa(s.seq) }

// ---- test helpers (lock-free callers: tests call them between requests) ----

func (s *Server) AddSpace(key, id string) { s.mu.Lock(); s.spaces[key] = id; s.mu.Unlock() }

func (s *Server) AddPage(p Page) *Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = s.next()
	}
	if p.Version == 0 {
		p.Version = 1
	}
	if p.AuthorID == "" {
		p.AuthorID = "me"
	}
	if p.CreatedAt == "" {
		p.CreatedAt = "2026-01-01T10:00:00.000Z"
	}
	if p.UpdatedAt == "" {
		p.UpdatedAt = p.CreatedAt
	}
	s.pages[p.ID] = &p
	return &p
}

func (s *Server) AddComment(c Comment) *Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = s.next()
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.AuthorID == "" {
		c.AuthorID = "bob"
	}
	if c.CreatedAt == "" {
		c.CreatedAt = "2026-02-01T10:00:00.000Z"
	}
	s.comments[c.ID] = &c
	return &c
}

func (s *Server) EditPage(id string, f func(*Page)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pages[id]
	f(p)
	p.Version++
	p.AuthorID = "bob"
	p.UpdatedAt = s.now
}

func (s *Server) Page(id string) (*Page, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pages[id]
	return p, ok
}

func (s *Server) DeletePage(id string) { s.mu.Lock(); delete(s.pages, id); s.mu.Unlock() }

func (s *Server) Comments(pageID string) []*Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commentsOf(pageID)
}

func (s *Server) commentsOf(pageID string) []*Comment {
	var out []*Comment
	for _, c := range s.comments {
		if c.PageID == pageID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg})
}

func storage(v string) map[string]any {
	return map[string]any{"storage": map[string]any{"representation": "storage", "value": v}}
}

func (p *Page) json(withBody bool) map[string]any {
	m := map[string]any{"id": p.ID, "status": "current", "title": p.Title, "spaceId": p.SpaceID,
		"authorId": p.AuthorID, "createdAt": p.CreatedAt,
		"version": map[string]any{"number": p.Version, "authorId": p.AuthorID, "createdAt": p.UpdatedAt}}
	if p.ParentID != "" {
		m["parentId"] = p.ParentID
	} else {
		m["parentId"] = nil
	}
	if withBody {
		m["body"] = storage(p.Storage)
	}
	return m
}

func (c *Comment) json() map[string]any {
	return map[string]any{"id": c.ID, "status": "current", "pageId": c.PageID, "body": storage(c.Storage),
		"version": map[string]any{"number": c.Version, "authorId": c.AuthorID, "createdAt": c.CreatedAt}}
}

// paged writes one page of items; the cursor is a plain offset.
func (s *Server) paged(w http.ResponseWriter, r *http.Request, items []any) {
	limit := s.PageLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l < limit {
		limit = l
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(off+limit, len(items))
	resp := map[string]any{"results": items[min(off, len(items)):end], "_links": map[string]any{}}
	if end < len(items) {
		q := r.URL.Query()
		q.Set("cursor", strconv.Itoa(end))
		resp["_links"] = map[string]any{"next": r.URL.Path + "?" + q.Encode()}
	}
	writeJSON(w, 200, resp)
}

type bodyIn struct {
	Representation string `json:"representation"`
	Value          string `json:"value"`
}

type pageIn struct {
	ID, Status, Title, SpaceID, ParentID string
	Body                                 bodyIn
	Version                              struct{ Number int }
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func (s *Server) titleTaken(spaceID, title, except string) bool {
	for _, p := range s.pages {
		if p.SpaceID == spaceID && p.Title == title && p.ID != except {
			return true
		}
	}
	return false
}

// ---- handlers ----

func (s *Server) getSpaces(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("keys")
	var out []any
	if id, ok := s.spaces[key]; ok {
		home := ""
		for _, p := range s.pages {
			if p.SpaceID == id && p.ParentID == "" && (home == "" || p.ID < home) {
				home = p.ID
			}
		}
		out = append(out, map[string]any{"id": id, "key": key, "homepageId": home})
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (s *Server) sortedPages(spaceID string) []*Page {
	var ps []*Page
	for _, p := range s.pages {
		if p.SpaceID == spaceID {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	return ps
}

func (s *Server) listPages(w http.ResponseWriter, r *http.Request) {
	var items []any
	for _, p := range s.sortedPages(r.PathValue("id")) {
		items = append(items, p.json(false))
	}
	s.paged(w, r, items)
}

func (s *Server) getPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	writeJSON(w, 200, p.json(r.URL.Query().Get("body-format") == "storage"))
}

func (s *Server) createPage(w http.ResponseWriter, r *http.Request) {
	var in pageIn
	if err := decode(r, &in); err != nil || in.Title == "" || in.SpaceID == "" {
		fail(w, 400, "title and spaceId are required")
		return
	}
	if s.titleTaken(in.SpaceID, in.Title, "") {
		fail(w, 400, "A page with this title already exists")
		return
	}
	if in.ParentID != "" {
		if _, ok := s.pages[in.ParentID]; !ok {
			fail(w, 400, "parent not found")
			return
		}
	}
	p := &Page{ID: s.next(), Title: in.Title, ParentID: in.ParentID, SpaceID: in.SpaceID, Storage: in.Body.Value,
		AuthorID: "me", CreatedAt: s.now, UpdatedAt: s.now, Version: 1}
	s.pages[p.ID] = p
	writeJSON(w, 200, p.json(true))
}

func (s *Server) updatePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var in pageIn
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Version.Number != p.Version+1 {
		fail(w, 409, fmt.Sprintf("Version must be incremented when updating a page. Current Version: [%d]. Provided version: [%d]", p.Version, in.Version.Number))
		return
	}
	if s.titleTaken(p.SpaceID, in.Title, p.ID) {
		fail(w, 400, "A page with this title already exists")
		return
	}
	if in.ParentID != "" {
		if _, ok := s.pages[in.ParentID]; !ok {
			fail(w, 400, "parent not found")
			return
		}
		p.ParentID = in.ParentID
	}
	p.Title, p.Storage = in.Title, in.Body.Value
	p.Version++
	p.AuthorID, p.UpdatedAt = "me", s.now
	writeJSON(w, 200, p.json(true))
}

func (s *Server) deletePage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	delete(s.pages, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) getLabels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for i, l := range p.Labels {
		items = append(items, map[string]any{"id": strconv.Itoa(i + 1), "name": l, "prefix": "global"})
	}
	s.paged(w, r, items)
}

func (s *Server) getComments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.pages[r.PathValue("id")]; !ok {
		fail(w, 404, "page not found")
		return
	}
	var items []any
	for _, c := range s.commentsOf(r.PathValue("id")) {
		items = append(items, c.json())
	}
	s.paged(w, r, items)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PageID string `json:"pageId"`
		Body   bodyIn
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, ok := s.pages[in.PageID]; !ok {
		fail(w, 404, "page not found")
		return
	}
	c := &Comment{ID: s.next(), PageID: in.PageID, Storage: in.Body.Value, AuthorID: "me", CreatedAt: s.now, Version: 1}
	s.comments[c.ID] = c
	writeJSON(w, 200, c.json())
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) {
	c, ok := s.comments[r.PathValue("id")]
	if !ok {
		fail(w, 404, "comment not found")
		return
	}
	var in struct {
		Body    bodyIn
		Version struct{ Number int }
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.Version.Number != c.Version+1 {
		fail(w, 409, "version conflict")
		return
	}
	c.Storage, c.Version = in.Body.Value, c.Version+1
	writeJSON(w, 200, c.json())
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.comments[r.PathValue("id")]; !ok {
		fail(w, 404, "comment not found")
		return
	}
	delete(s.comments, r.PathValue("id"))
	w.WriteHeader(204)
}

func (s *Server) addLabel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	var in []struct{ Prefix, Name string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	for _, l := range in {
		dup := false
		for _, have := range p.Labels {
			dup = dup || have == l.Name
		}
		if !dup {
			p.Labels = append(p.Labels, l.Name)
		}
	}
	writeJSON(w, 200, map[string]any{"results": []any{}})
}

func (s *Server) removeLabel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pages[r.PathValue("id")]
	if !ok {
		fail(w, 404, "page not found")
		return
	}
	name := r.URL.Query().Get("name")
	var kept []string
	for _, l := range p.Labels {
		if l != name {
			kept = append(kept, l)
		}
	}
	p.Labels = kept
	w.WriteHeader(204)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("accountId")
	name, ok := s.users[id]
	if !ok {
		fail(w, 404, "user not found")
		return
	}
	writeJSON(w, 200, map[string]any{"accountId": id, "displayName": name, "publicName": name})
}
```

`internal/adapter/confluence/url.go`:

```go
package confluence

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type target struct{ base, host, space, email, token string }

func parseTarget(u *url.URL, cfg map[string]string, getenv func(string) string) (target, error) {
	space := strings.Trim(u.Path, "/")
	if u.Host == "" || space == "" || strings.Contains(space, "/") {
		return target{}, fmt.Errorf("bad remote %q: want confluence://<host>/<SPACEKEY>", u.String())
	}
	t := target{host: u.Hostname(), space: space, base: "https://" + u.Host}
	if b := cfg["base"]; b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	if b := u.Query().Get("base"); b != "" {
		t.base = strings.TrimRight(b, "/")
	}
	t.email = getenv("GFS_CONFLUENCE_EMAIL")
	if t.email == "" && u.User != nil {
		t.email = u.User.Username()
	}
	if t.email == "" {
		t.email = cfg["email"]
	}
	t.token = getenv("GFS_CONFLUENCE_TOKEN")
	if t.token == "" {
		return target{}, errors.New("set GFS_CONFLUENCE_TOKEN to an Atlassian API token")
	}
	if t.email == "" {
		return target{}, errors.New("set GFS_CONFLUENCE_EMAIL, put the email in the URL (confluence://me%40x.com@host/SPACE), or set [remote] email")
	}
	return t, nil
}
```

`internal/adapter/confluence/client.go`:

```go
package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var m struct{ Message string }
	if json.Unmarshal([]byte(e.Body), &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return fmt.Sprintf("confluence: HTTP %d: %s", e.Status, msg)
}

func codeOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return strconv.Itoa(ae.Status)
	}
	return ""
}

type client struct {
	t  target
	hc *http.Client
}

func newClient(t target) *client { return &client{t: t, hc: &http.Client{Timeout: 60 * time.Second}} }

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.t.base+path, body)
	if err != nil {
		return err
	}
	if c.t.email != "" || c.t.token != "" {
		req.SetBasicAuth(c.t.email, c.t.token)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		ae := &APIError{Status: resp.StatusCode, Body: string(data)}
		switch resp.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", adapter.ErrNotFound, ae)
		case http.StatusConflict:
			return fmt.Errorf("%w: %w", adapter.ErrLock, ae)
		}
		return ae
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *client) paginate(ctx context.Context, path string, each func(json.RawMessage) error) error {
	for path != "" {
		var resp struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return err
		}
		for _, r := range resp.Results {
			if err := each(r); err != nil {
				return err
			}
		}
		path = resp.Links.Next
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: fake server, target parsing, REST client"
```

---
### Task 17: Confluence schema and storage-format conversion

Pure functions, no HTTP. The body is Confluence storage format passed through untouched in meaning: it is parsed into the tree inside `<body>` (so it is canonical, diffable and mergeable), and serialised back by `storageOf` when sent. `storageOf` does **not** use the canonical printer: files at rest use CDATA for text with `<`/`&` (spec 4.2), but Confluence storage expects entity-escaped text everywhere except inside `ac:plain-text-body` / `ac:plain-text-link-body`, which must stay CDATA. `storageOf` therefore writes children compactly, as they are in the tree (whitespace text nodes included), escaping `&`, `<`, `>` in text (CDATA inside the two plain-text elements) and `&`, `<`, `>`, `"`, newline, tab, CR in attribute values.

Mapping (API → `<page>`):

- root attributes: `id` = page id, `version` = `version.number`, `parent` = `parentId` (omitted when null), `created` = `createdAt`, `updated` = `version.createdAt`. All read-only.
- `<title>` = `title`.
- `<labels>` = labels with `prefix == "global"`, one `<label>` each (sorted by canon).
- `<body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">` + parsed storage value. Always present (empty storage → empty `<body .../>`).
- `<comment id= author= created= version=>` + parsed storage value of each footer comment. `author` = display name of `version.authorId`, `created` = `version.createdAt`, `version` = `version.number`. All read-only.

**Files:**
- Create: `internal/adapter/confluence/schema.go`, `internal/adapter/confluence/convert.go`
- Test: `internal/adapter/confluence/convert_test.go`

**Interfaces:**
- Consumes: `schema`, `canon`, `xmltree`.
- Produces:
  - `var pageSchema *schema.Schema`; constants `nsAC`, `nsRI`, `bodyType`
  - `type apiVersion struct{ Number int; AuthorID, CreatedAt string }` (json tags `number`, `authorId`, `createdAt`)
  - `type apiBody struct{ Storage struct{ Value string } }` (json `storage.value`)
  - `type apiPage struct{ ID, Title, SpaceID, ParentID, CreatedAt string; Version apiVersion; Body apiBody }`
  - `type apiComment struct{ ID string; Version apiVersion; Body apiBody }`
  - `func pageNode(p apiPage, labels []string, comments []apiComment, name func(accountID string) string) (*xmltree.Node, error)`
  - `func parseStorage(elem, value string) (*xmltree.Node, error)` — element `elem` containing the parsed storage
  - `func storageOf(n *xmltree.Node) string` — storage-format serialisation of the children of `n`; `""` for nil
  - `func titleOf(root *xmltree.Node) string`, `func labelsOf(root *xmltree.Node) []string`

- [ ] **Step 1: Write the failing test** `internal/adapter/confluence/convert_test.go`

```go
package confluence

import (
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

func names(id string) string { return map[string]string{"bob-id": "bob"}[id] }

func TestPageNodeGolden(t *testing.T) {
	p := apiPage{ID: "98120", Title: "Architecture", ParentID: "98001", CreatedAt: "2025-11-03T09:15:00.000Z",
		Version: apiVersion{Number: 8, AuthorID: "bob-id", CreatedAt: "2026-09-22T14:03:00.000Z"}}
	p.Body.Storage.Value = `<p>Hi <ac:link><ri:page ri:content-title="X"/></ac:link></p><ac:structured-macro ac:name="info"><ac:rich-text-body><p>n</p></ac:rich-text-body></ac:structured-macro>`
	c := apiComment{ID: "7731", Version: apiVersion{Number: 1, AuthorID: "bob-id", CreatedAt: "2026-09-16T17:10:00.000Z"}}
	c.Body.Storage.Value = "<p>Out of date.</p>"
	n, err := pageNode(p, []string{"b", "a"}, []apiComment{c}, names)
	if err != nil {
		t.Fatal(err)
	}
	canon.Normalize(n, pageSchema)
	want := `<page id="98120" version="8" parent="98001" created="2025-11-03T09:15:00.000Z" updated="2026-09-22T14:03:00.000Z">
  <title>Architecture</title>
  <labels>
    <label>a</label>
    <label>b</label>
  </labels>
  <body type="application/xhtml+xml" xmlns:ac="http://atlassian.com/content" xmlns:ri="http://atlassian.com/resource/identifier">
    <p>Hi <ac:link><ri:page ri:content-title="X"/></ac:link></p>
    <ac:structured-macro ac:name="info">
      <ac:rich-text-body>
        <p>n</p>
      </ac:rich-text-body>
    </ac:structured-macro>
  </body>
  <comment id="7731" author="bob" created="2026-09-16T17:10:00.000Z" version="1">
    <p>Out of date.</p>
  </comment>
</page>`
	if got := xmltree.Print(n, 0); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if titleOf(n) != "Architecture" || strings.Join(labelsOf(n), ",") != "a,b" {
		t.Fatal("titleOf/labelsOf")
	}
}

func TestStorageRoundTrip(t *testing.T) {
	cases := []string{
		`<p>a&nbsp;b &amp; c</p>`,
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[./deploy.sh && echo <ok>]]></ac:plain-text-body></ac:structured-macro>`,
		`<table><tbody><tr><td data-highlight-colour="#e3fcef">99.9%</td></tr></tbody></table>`,
		`plain text only`,
		``,
	}
	for _, in := range cases {
		n, err := parseStorage("body", in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		once := storageOf(n)
		n2, err := parseStorage("body", once)
		if err != nil {
			t.Fatalf("reparse %q: %v", once, err)
		}
		if twice := storageOf(n2); once != twice {
			t.Fatalf("not stable:\n%s\n%s", once, twice)
		}
	}
	n, _ := parseStorage("body", cases[1])
	if !strings.Contains(storageOf(n), "<![CDATA[./deploy.sh && echo <ok>]]>") {
		t.Fatal(storageOf(n))
	}
	n, _ = parseStorage("body", cases[0])
	if got := storageOf(n); got != "<p>a\u00a0b &amp; c</p>" {
		t.Fatalf("text must be entity-escaped, not CDATA: %q", got)
	}
	if storageOf(nil) != "" {
		t.Fatal("nil")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/ -run 'PageNode|Storage'`
Expected: FAIL, undefined `pageNode`.

- [ ] **Step 3: Implement** `internal/adapter/confluence/schema.go`

```go
package confluence

import "github.com/KrzysztofBogdan/gitfs/internal/schema"

const (
	nsAC     = "http://atlassian.com/content"
	nsRI     = "http://atlassian.com/resource/identifier"
	bodyType = "application/xhtml+xml"
)

var pageSchema = &schema.Schema{
	Root: "page", ID: "id", Version: "version",
	RootAttrs: []schema.Attr{
		{Name: "id", ReadOnly: true}, {Name: "version", ReadOnly: true}, {Name: "parent", ReadOnly: true},
		{Name: "created", ReadOnly: true}, {Name: "updated", ReadOnly: true},
	},
	Elems: []schema.Elem{
		{Name: "title", Kind: schema.Field},
		{Name: "labels", Kind: schema.List, Item: "label", Sorted: true},
		{Name: "body", Kind: schema.Body, BodyTypes: []string{bodyType},
			Attrs: []schema.Attr{{Name: "type"}, {Name: "xmlns:ac"}, {Name: "xmlns:ri"}}},
		{Name: "comment", Kind: schema.Sub, ID: "id", SortKey: "created",
			Attrs: []schema.Attr{{Name: "id", ReadOnly: true}, {Name: "author", ReadOnly: true},
				{Name: "created", ReadOnly: true}, {Name: "version", ReadOnly: true}}},
	},
}
```

`internal/adapter/confluence/convert.go`:

```go
package confluence

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiVersion struct {
	Number    int    `json:"number"`
	AuthorID  string `json:"authorId"`
	CreatedAt string `json:"createdAt"`
}

type apiBody struct {
	Storage struct {
		Value string `json:"value"`
	} `json:"storage"`
}

type apiPage struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	SpaceID   string     `json:"spaceId"`
	ParentID  string     `json:"parentId"`
	CreatedAt string     `json:"createdAt"`
	Version   apiVersion `json:"version"`
	Body      apiBody    `json:"body"`
}

type apiComment struct {
	ID      string     `json:"id"`
	Version apiVersion `json:"version"`
	Body    apiBody    `json:"body"`
}

func el(name string, attrs ...string) *xmltree.Node {
	n := &xmltree.Node{Kind: xmltree.Element, Name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i+1] != "" {
			n.SetAttr(attrs[i], attrs[i+1])
		}
	}
	return n
}

func textEl(name, text string) *xmltree.Node {
	n := el(name)
	if text != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
	}
	return n
}

// parseStorage parses a storage-format fragment into the children of <elem>.
func parseStorage(elem, value string) (*xmltree.Node, error) {
	n, err := xmltree.ParseString(fmt.Sprintf(`<%s xmlns:ac=%q xmlns:ri=%q>%s</%s>`, elem, nsAC, nsRI, value, elem))
	if err != nil {
		return nil, fmt.Errorf("storage format: %w", err)
	}
	n.Attrs = nil
	return n, nil
}

var (
	textEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;",
		"\n", "&#10;", "\r", "&#13;", "\t", "&#9;")
)

// storageOf serialises the children of n as Confluence storage format.
func storageOf(n *xmltree.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range n.Children {
		writeStorage(&b, c, n.Name)
	}
	return b.String()
}

func writeStorage(b *strings.Builder, n *xmltree.Node, parent string) {
	switch n.Kind {
	case xmltree.Text:
		if parent == "ac:plain-text-body" || parent == "ac:plain-text-link-body" {
			b.WriteString("<![CDATA[" + strings.ReplaceAll(n.Text, "]]>", "]]]]><![CDATA[>") + "]]>")
		} else {
			b.WriteString(textEsc.Replace(n.Text))
		}
	case xmltree.Comment:
		b.WriteString("<!--" + n.Text + "-->")
	case xmltree.Element:
		b.WriteString("<" + n.Name)
		for _, a := range n.Attrs {
			b.WriteString(" " + a.Name + `="` + attrEsc.Replace(a.Value) + `"`)
		}
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteByte('>')
		for _, c := range n.Children {
			writeStorage(b, c, n.Name)
		}
		b.WriteString("</" + n.Name + ">")
	}
}

func pageNode(p apiPage, labels []string, comments []apiComment, name func(string) string) (*xmltree.Node, error) {
	root := el("page", "id", p.ID, "version", strconv.Itoa(p.Version.Number), "parent", p.ParentID,
		"created", p.CreatedAt, "updated", p.Version.CreatedAt)
	root.Children = append(root.Children, textEl("title", p.Title))
	if len(labels) > 0 {
		ls := el("labels")
		for _, l := range labels {
			ls.Children = append(ls.Children, textEl("label", l))
		}
		root.Children = append(root.Children, ls)
	}
	body, err := parseStorage("body", p.Body.Storage.Value)
	if err != nil {
		return nil, fmt.Errorf("page %s: %w", p.ID, err)
	}
	body.Attrs = []xmltree.Attr{{Name: "type", Value: bodyType}, {Name: "xmlns:ac", Value: nsAC}, {Name: "xmlns:ri", Value: nsRI}}
	root.Children = append(root.Children, body)
	for _, c := range comments {
		cn, err := parseStorage("comment", c.Body.Storage.Value)
		if err != nil {
			return nil, fmt.Errorf("comment %s: %w", c.ID, err)
		}
		author := name(c.Version.AuthorID)
		if author == "" {
			author = c.Version.AuthorID
		}
		cn.Attrs = el("", "id", c.ID, "author", author, "created", c.Version.CreatedAt,
			"version", strconv.Itoa(c.Version.Number)).Attrs
		root.Children = append(root.Children, cn)
	}
	return root, nil
}

func titleOf(root *xmltree.Node) string {
	if t := root.Child("title"); t != nil {
		return t.TextContent()
	}
	return ""
}

func labelsOf(root *xmltree.Node) []string {
	var out []string
	if ls := root.Child("labels"); ls != nil {
		for _, l := range ls.ChildrenNamed("label") {
			out = append(out, l.TextContent())
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: page schema and storage-format conversion"
```

---
### Task 18: Confluence page tree → paths

Rules:

- `sanitize(title)`: `/` and `\` → `-`; control characters (`< 0x20`, `0x7f`) → space; trim spaces; a leading `.` → `_` (so no page can become hidden or collide with `.gfs`); empty → `untitled`.
- A page whose `Parent` is not a page in the space (null, or a non-page parent) is top-level: `<spaceDir>/<name>.xml`. Otherwise `<parent path without .xml>/<name>.xml`.
- Siblings whose sanitised names collide case-insensitively are de-duplicated: ordered by numeric id (shorter id first, then lexical), the first keeps the name, the next get ` (2)`, ` (3)`, …
- Parent cycles (corrupt data) are cut: a page more than 64 levels deep is treated as top-level.
- `parentPath(p)`: `eng/Home/Runbooks/Rollback.xml` → `eng/Home/Runbooks.xml`; a top-level path (`eng/Home.xml`) → `""`.

**Files:**
- Create: `internal/adapter/confluence/paths.go`
- Test: `internal/adapter/confluence/paths_test.go`

**Interfaces:**
- Produces:
  - `type pageRef struct{ ID, Title, Parent string }`
  - `func sanitize(title string) string`
  - `func pagePaths(spaceDir string, pages []pageRef) map[string]string` (id → path)
  - `func parentPath(p string) string`

- [ ] **Step 1: Write the failing test** `internal/adapter/confluence/paths_test.go`

```go
package confluence

import "testing"

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Architecture":    "Architecture",
		"CI/CD \\ notes":  "CI-CD - notes",
		".hidden":         "_hidden",
		"  ":              "untitled",
		"tab\there\x7f":   "tab here",
		"Login page: 500": "Login page: 500",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPagePaths(t *testing.T) {
	got := pagePaths("eng", []pageRef{
		{"98001", "Home", ""},
		{"98120", "Architecture", "98001"},
		{"98130", "Runbooks", "98001"},
		{"98871", "Rollback", "98130"},
		{"99", "Orphan", "55555"},
		{"100", "Dup", "98001"},
		{"1000", "dup", "98001"},
		{"7", "a", "8"},
		{"8", "b", "7"},
	})
	want := map[string]string{
		"98001": "eng/Home.xml",
		"98120": "eng/Home/Architecture.xml",
		"98130": "eng/Home/Runbooks.xml",
		"98871": "eng/Home/Runbooks/Rollback.xml",
		"99":    "eng/Orphan.xml",
		"100":   "eng/Home/Dup.xml",
		"1000":  "eng/Home/dup (2).xml",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: got %q, want %q", id, got[id], w)
		}
	}
	if got["7"] == "" || got["8"] == "" {
		t.Error("cycle members must still get a path")
	}
}

func TestParentPath(t *testing.T) {
	if parentPath("eng/Home/Runbooks/Rollback.xml") != "eng/Home/Runbooks.xml" || parentPath("eng/Home.xml") != "" {
		t.Fatal("parentPath")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/ -run 'Sanitize|PagePaths|ParentPath'`
Expected: FAIL, undefined `sanitize`.

- [ ] **Step 3: Implement** `internal/adapter/confluence/paths.go`

```go
package confluence

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

type pageRef struct{ ID, Title, Parent string }

func sanitize(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "untitled"
	}
	if s[0] == '.' {
		s = "_" + s[1:]
	}
	return s
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func pagePaths(spaceDir string, pages []pageRef) map[string]string {
	byID := map[string]pageRef{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	// depth-limited parent resolution; cycles and orphans become top-level
	parentOf := func(p pageRef) string {
		seen := 0
		for cur := p; ; {
			par, ok := byID[cur.Parent]
			if !ok {
				break
			}
			if seen++; seen > 64 || par.ID == p.ID {
				return ""
			}
			cur = par
		}
		if _, ok := byID[p.Parent]; ok {
			return p.Parent
		}
		return ""
	}
	children := map[string][]pageRef{}
	for _, p := range pages {
		children[parentOf(p)] = append(children[parentOf(p)], p)
	}
	out := map[string]string{}
	var walk func(parent, dir string, depth int)
	walk = func(parent, dir string, depth int) {
		kids := children[parent]
		sort.Slice(kids, func(i, j int) bool { return idLess(kids[i].ID, kids[j].ID) })
		used := map[string]int{}
		for _, k := range kids {
			name := sanitize(k.Title)
			key := strings.ToLower(name)
			used[key]++
			if n := used[key]; n > 1 {
				name = fmt.Sprintf("%s (%d)", name, n)
			}
			out[k.ID] = dir + "/" + name + ".xml"
			if depth < 64 {
				walk(k.ID, dir+"/"+name, depth+1)
			}
		}
	}
	walk("", spaceDir, 0)
	for _, p := range pages { // cycle members never reached from the top
		if _, ok := out[p.ID]; !ok {
			out[p.ID] = spaceDir + "/" + sanitize(p.Title) + " (" + p.ID + ").xml"
		}
	}
	return out
}

func parentPath(p string) string {
	dir := path.Dir(p)
	if !strings.Contains(dir, "/") {
		return ""
	}
	return dir + ".xml"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence
git commit -m "confluence: page tree to paths with sanitising and de-duplication"
```

---
### Task 19: Confluence adapter and session

Behaviour:

- `Open`: `parseTarget(u, cfg, os.Getenv)`, `GET spaces?keys=<KEY>` (not found → error `space <KEY> not found or not visible`), `spaceDir = strings.ToLower(key)`.
- `List`: load the page tree (id, title, parent) with pagination, compute all paths, then fetch every page fully (page + labels + comments). `Full: true`, no cursor. (Full fetch is simple and correct: label and comment changes do not bump the page version, so version stubs would miss them. Clone size is an open question in the spec.)
- `Fetch(id)`: `GET page` (`ErrNotFound` passes through), update the page's entry in the cached tree (title/parent may have changed), recompute paths, then labels, comments, author names (cached `GET user`), `pageNode`. `By` = display name of `version.authorId`, `At` = `version.createdAt`, `Version` = version number.
- `Apply`, resource `create`: title = `<title>`, or the file's base name when `<title>` is empty. The path's first segment must be `spaceDir` (else error `new pages must live under <spaceDir>/`). Parent = `parentPath(path)`: `""` → no `parentId`; otherwise `IDByPath(parent)`, missing → error `parent page <parent> is not on the remote yet; commit it first`. `POST pages`. Then add labels, then create every comment in the file. A failed label/comment step after the page exists is returned as an **extra** result (`update labels` / `create comment[n]`) so the engine keeps the local form of just that part. `Result.ID` = new page id, `Detail` = `id=<id>`. If Confluence places a top-level page elsewhere (e.g. under the homepage), write-back moves the file to the real path.
- `Apply`, updates: the actions `update title`, `update body` and `move` are combined into **one** `PUT page` sent first, with `version.number = Lock + 1`. A 409 returns a single result for the first action wrapping `ErrLock`, nothing else executed. Title: `<title>`; for a move whose `<title>` did not change, if `sanitize(title) != base name of the new path`, the page is renamed to the base name. Parent for a move: `IDByPath(parentPath(to))`; moving to the space root or to another space is an error. `Detail` = `v<n> -> v<n+1>`.
- `update labels`: add labels missing on the remote (one `POST` with all), remove labels no longer in the file (one `DELETE` each).
- `create comment[n]` → `POST footer-comments` with the n-th new comment's storage. `update comment[id=X]` → `PUT` with `version.number` = the comment's `version` attribute + 1. `delete comment[id=X]` → `DELETE`.
- resource `delete` → `DELETE page`.
- Any other verb → error `confluence has no action "<verb>"`.
- `Check` (dry run, read-only): same validations as Apply without writes — parent exists, space prefix, move target, and **title unique in the space** for creates and renames (Confluence rejects duplicates).
- `Describe`: class = verb; details `create page under "<Parent>"` / `create top-level page`, `rename page to "<title>"`, `update body`, `update labels`, `add comment`, `edit comment <id>`, `delete comment <id>`, `move under "<Parent>"` / `rename to "<name>"`, `delete page`.
- Registration: `func init() { adapter.Register(&Adapter{}) }`; `internal/cli/root.go` blank-imports the package.

**Files:**
- Create: `internal/adapter/confluence/adapter.go`, `internal/adapter/confluence/session.go`
- Modify: `internal/cli/root.go` (add `_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence"`)
- Test: `internal/adapter/confluence/session_test.go`

**Interfaces:**
- Consumes: Tasks 16–18, `adapter` (Task 8), `validate.FindSub` (Task 4), `changes.ParseTarget` (Task 12).
- Produces: `type Adapter struct{}` implementing `adapter.Adapter` for scheme `confluence`; `func openSession(ctx context.Context, t target) (*session, error)` (used by tests).

- [ ] **Step 1: Write the failing test** `internal/adapter/confluence/session_test.go`

```go
package confluence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

var bg = context.Background()

// space builds ENG with Home > {Architecture, Runbooks}.
func space(t *testing.T) (*cftest.Server, *session) {
	t.Helper()
	s := cftest.New()
	t.Cleanup(s.Close)
	s.AddSpace("ENG", "100")
	s.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Engineering space.</p>", Labels: []string{"index"}})
	s.AddPage(cftest.Page{ID: "98120", Title: "Architecture", ParentID: "98001", SpaceID: "100", Storage: "<p>a</p><p>b</p>"})
	s.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	s.AddComment(cftest.Comment{ID: "7731", PageID: "98120", Storage: "<p>Out of date.</p>"})
	sess, err := openSession(bg, target{base: s.URL, space: "ENG", email: "me@x.com", token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return s, sess
}

func byPath(l adapter.Listing) map[string]adapter.Resource {
	m := map[string]adapter.Resource{}
	for _, r := range l.Resources {
		m[r.Path] = r
	}
	return m
}

func TestListAndFetch(t *testing.T) {
	_, sess := space(t)
	l, err := sess.List(bg, "")
	if err != nil || !l.Full || len(l.Resources) != 3 {
		t.Fatalf("%+v %v", l, err)
	}
	m := byPath(l)
	arch, ok := m["eng/Home/Architecture.xml"]
	if !ok || arch.ID != "98120" || arch.Version != "1" {
		t.Fatalf("%v", m)
	}
	got := xmltree.Print(arch.Root, 0)
	for _, want := range []string{`parent="98001"`, "<title>Architecture</title>", `<comment id="7731" author="bob"`, "<p>Out of date.</p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if _, ok := m["eng/Home.xml"]; !ok {
		t.Fatal("Home.xml")
	}
	if _, err := sess.Fetch(bg, "424242"); !errors.Is(err, adapter.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func resource(t *testing.T, sess *session, id string) *adapter.Resource {
	t.Helper()
	r, err := sess.Fetch(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ids(m map[string]string) func(string) (string, bool) {
	return func(p string) (string, bool) { id, ok := m[p]; return id, ok }
}

var known = ids(map[string]string{"eng/Home.xml": "98001", "eng/Home/Runbooks.xml": "98130", "eng/Home/Architecture.xml": "98120"})

func apply(sess *session, local, base *adapter.Resource, acts ...adapter.Action) []adapter.Result {
	req := adapter.ApplyRequest{Local: local, Base: base, Actions: acts, IDByPath: known}
	if base != nil {
		req.Lock = base.Version
	}
	return sess.Apply(bg, req)
}

func setText(n *xmltree.Node, name, text string) {
	c := n.Child(name)
	c.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: text}}
}

func TestUpdateTitleBodyAndLock(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	setText(local.Root, "title", "Architecture v2")
	local.Root.Child("body").Children = local.Root.Child("body").Children[:1]
	res := apply(sess, local, base, adapter.Action{Verb: "update", Group: "title"}, adapter.Action{Verb: "update", Group: "body"})
	if res[0].Err != nil || res[1].Err != nil || res[0].Detail != "v1 -> v2" {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	if p.Title != "Architecture v2" || p.Storage != "<p>a</p>" || p.Version != 2 {
		t.Fatalf("%+v", p)
	}
	stale := apply(sess, local, base, adapter.Action{Verb: "update", Group: "body"})
	if len(stale) != 1 || !errors.Is(stale[0].Err, adapter.ErrLock) {
		t.Fatalf("want ErrLock, got %+v", stale)
	}
}

func TestCreateUnderParent(t *testing.T) {
	srv, sess := space(t)
	root, _ := xmltree.ParseString(`<page><title>Rollback</title><labels><label>runbook</label></labels>` +
		`<body type="application/xhtml+xml"><ol><li>Redeploy <code>v2.3.1</code></li></ol></body><comment><p>draft</p></comment></page>`)
	res := apply(sess, &adapter.Resource{Path: "eng/Home/Runbooks/Rollback.xml", Root: root}, nil, adapter.Action{Verb: "create"})
	if len(res) != 1 || res[0].Err != nil || res[0].ID == "" {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page(res[0].ID)
	if p.ParentID != "98130" || p.Title != "Rollback" || strings.Join(p.Labels, ",") != "runbook" || !strings.Contains(p.Storage, "<code>v2.3.1</code>") {
		t.Fatalf("%+v", p)
	}
	if cs := srv.Comments(res[0].ID); len(cs) != 1 || cs[0].Storage != "<p>draft</p>" {
		t.Fatalf("%+v", cs)
	}
	got := resource(t, sess, res[0].ID)
	if got.Path != "eng/Home/Runbooks/Rollback.xml" {
		t.Fatal(got.Path)
	}
	orphan := apply(sess, &adapter.Resource{Path: "eng/Nope/X.xml", Root: root}, nil, adapter.Action{Verb: "create"})
	if orphan[0].Err == nil || !strings.Contains(orphan[0].Err.Error(), "commit it first") {
		t.Fatalf("%+v", orphan)
	}
}

func TestMoveAndRename(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: "eng/Home/Runbooks/Arch.xml", Root: base.Root.Clone()}
	res := apply(sess, local, base, adapter.Action{Verb: "move", From: base.Path, To: local.Path})
	if res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	if p.ParentID != "98130" || p.Title != "Arch" {
		t.Fatalf("%+v", p)
	}
	if got := resource(t, sess, "98120"); got.Path != "eng/Home/Runbooks/Arch.xml" {
		t.Fatal(got.Path)
	}
}

func TestLabelsCommentsDelete(t *testing.T) {
	srv, sess := space(t)
	base := resource(t, sess, "98120")
	local := &adapter.Resource{ID: base.ID, Path: base.Path, Root: base.Root.Clone()}
	lbl, _ := xmltree.ParseString(`<labels><label>architecture</label></labels>`)
	local.Root.Children = append(local.Root.Children, lbl)
	c := local.Root.Child("comment")
	c.Children = []*xmltree.Node{{Kind: xmltree.Element, Name: "p", Children: []*xmltree.Node{{Kind: xmltree.Text, Text: "Fixed."}}}}
	res := apply(sess, local, base,
		adapter.Action{Verb: "update", Group: "labels"},
		adapter.Action{Verb: "update", Target: "comment[id=7731]"})
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	p, _ := srv.Page("98120")
	cs := srv.Comments("98120")
	if strings.Join(p.Labels, ",") != "architecture" || cs[0].Storage != "<p>Fixed.</p>" || cs[0].Version != 2 {
		t.Fatalf("%+v %+v", p, cs[0])
	}
	base = resource(t, sess, "98120")
	res = apply(sess, base, base, adapter.Action{Verb: "delete", Target: "comment[id=7731]"}, adapter.Action{Verb: "delete"})
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	if _, ok := srv.Page("98120"); ok || len(srv.Comments("98120")) != 0 {
		t.Fatal("not deleted")
	}
}

func TestCheckDuplicateTitle(t *testing.T) {
	_, sess := space(t)
	root, _ := xmltree.ParseString(`<page><title>Runbooks</title><body type="application/xhtml+xml"/></page>`)
	res := sess.Check(bg, adapter.ApplyRequest{Local: &adapter.Resource{Path: "eng/Home/Runbooks (2).xml", Root: root},
		Actions: []adapter.Action{{Verb: "create"}}, IDByPath: known})
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "already used") {
		t.Fatalf("%+v", res)
	}
}

func TestDescribe(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		act  adapter.Action
		path string
		want string
	}{
		{adapter.Action{Verb: "create"}, "eng/Home/Runbooks/Rollback.xml", `create page under "Runbooks"`},
		{adapter.Action{Verb: "create"}, "eng/Top.xml", "create top-level page"},
		{adapter.Action{Verb: "update", Group: "body"}, "eng/Home.xml", "update body"},
		{adapter.Action{Verb: "move", From: "eng/Home/A.xml", To: "eng/Home/B.xml"}, "eng/Home/B.xml", `rename to "B"`},
		{adapter.Action{Verb: "move", From: "eng/Home/A.xml", To: "eng/Home/Runbooks/A.xml"}, "", `move under "Runbooks"`},
		{adapter.Action{Verb: "delete"}, "", "delete page"},
	}
	for _, c := range cases {
		act := c.act
		root, _ := xmltree.ParseString(`<page><title>T</title></page>`)
		a.Describe(&act, &adapter.Resource{Path: c.path, Root: root})
		if act.Detail != c.want || act.Class != act.Verb {
			t.Errorf("%+v: got %q", c.act, act.Detail)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/adapter/confluence/ -run 'List|Update|Create|Move|Labels|Check|Describe'`
Expected: FAIL, undefined `openSession`.

- [ ] **Step 3: Implement** `internal/adapter/confluence/adapter.go`

```go
// Package confluence mirrors one Confluence Cloud space as a page tree of XML files.
package confluence

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/schema"
)

func init() { adapter.Register(&Adapter{}) }

type Adapter struct{}

func (*Adapter) Name() string                 { return "confluence" }
func (*Adapter) Schemes() []string            { return []string{"confluence"} }
func (*Adapter) Schema() *schema.Schema       { return pageSchema }
func (*Adapter) PathModel() adapter.PathModel { return adapter.Tree }
func (*Adapter) DefaultDir(*url.URL) string   { return "confluence" }
func (*Adapter) Verbs() []adapter.Verb        { return nil }

func (*Adapter) Open(ctx context.Context, u *url.URL, cfg map[string]string) (adapter.Session, error) {
	t, err := parseTarget(u, cfg, os.Getenv)
	if err != nil {
		return nil, err
	}
	return openSession(ctx, t)
}

func baseName(p string) string { return strings.TrimSuffix(path.Base(p), ".xml") }

func (*Adapter) Describe(a *adapter.Action, local *adapter.Resource) {
	a.Class = a.Verb
	switch {
	case a.Target != "":
		_, id, _, _ := changes.ParseTarget(a.Target)
		switch a.Verb {
		case "create":
			a.Detail = "add comment"
		case "update":
			a.Detail = "edit comment " + id
		case "delete":
			a.Detail = "delete comment " + id
		}
	case a.Verb == "create":
		if parent := parentPath(local.Path); parent != "" {
			a.Detail = fmt.Sprintf("create page under %q", baseName(parent))
		} else {
			a.Detail = "create top-level page"
		}
	case a.Verb == "update" && a.Group == "title":
		a.Detail = fmt.Sprintf("rename page to %q", titleOf(local.Root))
	case a.Verb == "update":
		a.Detail = "update " + a.Group
	case a.Verb == "move":
		if path.Dir(a.From) == path.Dir(a.To) {
			a.Detail = fmt.Sprintf("rename to %q", baseName(a.To))
		} else {
			a.Detail = fmt.Sprintf("move under %q", baseName(parentPath(a.To)))
		}
	case a.Verb == "delete":
		a.Detail = "delete page"
	default:
		a.Detail = a.Verb + " (not supported by confluence)"
	}
}
```

`internal/adapter/confluence/session.go`:

```go
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/changes"
	"github.com/KrzysztofBogdan/gitfs/internal/validate"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type session struct {
	c        *client
	spaceKey string
	spaceID  string
	spaceDir string
	tree     map[string]pageRef // nil until loaded
	names    map[string]string
}

func openSession(ctx context.Context, t target) (*session, error) {
	s := &session{c: newClient(t), spaceKey: t.space, spaceDir: strings.ToLower(t.space), names: map[string]string{}}
	var resp struct {
		Results []struct{ ID, Key string } `json:"results"`
	}
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/spaces?keys="+url.QueryEscape(t.space), nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("space %s not found or not visible", t.space)
	}
	s.spaceID = resp.Results[0].ID
	return s, nil
}

func (s *session) Close() error { return nil }

func (s *session) loadTree(ctx context.Context) error {
	tree := map[string]pageRef{}
	err := s.c.paginate(ctx, "/wiki/api/v2/spaces/"+s.spaceID+"/pages?limit=250", func(raw json.RawMessage) error {
		var p apiPage
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID}
		return nil
	})
	if err == nil {
		s.tree = tree
	}
	return err
}

func (s *session) paths() map[string]string {
	refs := make([]pageRef, 0, len(s.tree))
	for _, r := range s.tree {
		refs = append(refs, r)
	}
	return pagePaths(s.spaceDir, refs)
}

func (s *session) name(ctx context.Context, accountID string) string {
	if accountID == "" {
		return ""
	}
	if n, ok := s.names[accountID]; ok {
		return n
	}
	var u struct {
		DisplayName string `json:"displayName"`
	}
	n := accountID
	if s.c.do(ctx, http.MethodGet, "/wiki/rest/api/user?accountId="+url.QueryEscape(accountID), nil, &u) == nil && u.DisplayName != "" {
		n = u.DisplayName
	}
	s.names[accountID] = n
	return n
}

func (s *session) List(ctx context.Context, _ string) (adapter.Listing, error) {
	if err := s.loadTree(ctx); err != nil {
		return adapter.Listing{}, err
	}
	ids := make([]string, 0, len(s.tree))
	for id := range s.tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	l := adapter.Listing{Full: true}
	for _, id := range ids {
		r, err := s.Fetch(ctx, id)
		if errors.Is(err, adapter.ErrNotFound) {
			continue // deleted while listing
		}
		if err != nil {
			return adapter.Listing{}, err
		}
		l.Resources = append(l.Resources, *r)
	}
	return l, nil
}

func (s *session) Fetch(ctx context.Context, id string) (*adapter.Resource, error) {
	if s.tree == nil {
		if err := s.loadTree(ctx); err != nil {
			return nil, err
		}
	}
	var p apiPage
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/pages/"+id+"?body-format=storage", nil, &p); err != nil {
		if errors.Is(err, adapter.ErrNotFound) {
			delete(s.tree, id)
		}
		return nil, err
	}
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID}
	var labels []string
	err := s.c.paginate(ctx, "/wiki/api/v2/pages/"+id+"/labels?limit=250", func(raw json.RawMessage) error {
		var l struct{ Name, Prefix string }
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		if l.Prefix == "global" {
			labels = append(labels, l.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var comments []apiComment
	err = s.c.paginate(ctx, "/wiki/api/v2/pages/"+id+"/footer-comments?body-format=storage&limit=250", func(raw json.RawMessage) error {
		var c apiComment
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		comments = append(comments, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	root, err := pageNode(p, labels, comments, func(a string) string { return s.name(ctx, a) })
	if err != nil {
		return nil, err
	}
	return &adapter.Resource{ID: p.ID, Version: strconv.Itoa(p.Version.Number), Path: s.paths()[p.ID],
		By: s.name(ctx, p.Version.AuthorID), At: p.Version.CreatedAt, Root: root}, nil
}

func storageBody(v string) map[string]any { return map[string]any{"representation": "storage", "value": v} }

// titleTaken reports whether another page in the space already has title.
func (s *session) titleTaken(title, except string) bool {
	for _, r := range s.tree {
		if r.Title == title && r.ID != except {
			return true
		}
	}
	return false
}

func (s *session) parentFor(p string, idByPath func(string) (string, bool)) (string, error) {
	if !strings.HasPrefix(p, s.spaceDir+"/") {
		return "", fmt.Errorf("pages must live under %s/", s.spaceDir)
	}
	parent := parentPath(p)
	if parent == "" {
		return "", nil
	}
	id, ok := idByPath(parent)
	if !ok {
		return "", fmt.Errorf("parent page %s is not on the remote yet; commit it first", parent)
	}
	return id, nil
}

// pagePut computes the title and parent for the combined page update.
func (s *session) pagePut(req adapter.ApplyRequest, idx []int) (title, parent string, err error) {
	title = titleOf(req.Local.Root)
	titleChanged := false
	for _, i := range idx {
		a := req.Actions[i]
		titleChanged = titleChanged || (a.Verb == "update" && a.Group == "title")
	}
	for _, i := range idx {
		a := req.Actions[i]
		if a.Verb != "move" {
			continue
		}
		if parentPath(a.To) == "" {
			return "", "", errors.New("moving a page to the space root is not supported; move it under a page")
		}
		if parent, err = s.parentFor(a.To, req.IDByPath); err != nil {
			return "", "", err
		}
		if !titleChanged && sanitize(title) != baseName(a.To) {
			title = baseName(a.To)
		}
	}
	return title, parent, nil
}

func (s *session) Check(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	if s.tree == nil {
		if err := s.loadTree(ctx); err != nil {
			return []adapter.Result{{Action: req.Actions[0], Err: err}}
		}
	}
	var out []adapter.Result
	for i, a := range req.Actions {
		res := adapter.Result{Action: a}
		switch {
		case a.Target != "":
		case a.Verb == "create":
			title := titleOf(req.Local.Root)
			if title == "" {
				title = baseName(req.Local.Path)
			}
			if _, err := s.parentFor(req.Local.Path, req.IDByPath); err != nil {
				res.Err = err
			} else if s.titleTaken(title, "") {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, s.spaceKey)
			}
		case a.Verb == "move" || (a.Verb == "update" && a.Group == "title"):
			title, _, err := s.pagePut(req, []int{i})
			if err != nil {
				res.Err = err
			} else if s.titleTaken(title, req.Local.ID) {
				res.Err = fmt.Errorf("title %q is already used in space %s", title, s.spaceKey)
			}
		case a.Verb == "update" || a.Verb == "delete":
		default:
			res.Err = fmt.Errorf("confluence has no action %q", a.Verb)
		}
		out = append(out, res)
	}
	return out
}

func (s *session) Apply(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	if s.tree == nil {
		if err := s.loadTree(ctx); err != nil {
			return []adapter.Result{{Action: req.Actions[0], Err: err, Code: codeOf(err)}}
		}
	}
	out := make([]adapter.Result, len(req.Actions))
	var pageIdx, labelIdx, commentIdx []int
	for i, a := range req.Actions {
		out[i].Action = a
		switch {
		case a.Target != "":
			commentIdx = append(commentIdx, i)
		case a.Verb == "create":
			return s.create(ctx, req)
		case a.Verb == "delete":
			err := s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/pages/"+req.Base.ID, nil, nil)
			out[i].Err, out[i].Code = err, codeOf(err)
			delete(s.tree, req.Base.ID)
		case a.Verb == "update" && a.Group == "labels":
			labelIdx = append(labelIdx, i)
		case a.Verb == "move" || (a.Verb == "update" && (a.Group == "title" || a.Group == "body")):
			pageIdx = append(pageIdx, i)
		default:
			out[i].Err = fmt.Errorf("confluence has no action %q", a.Verb)
		}
	}
	id := req.Base.ID
	if len(pageIdx) > 0 {
		lock, _ := strconv.Atoi(req.Lock)
		title, parent, err := s.pagePut(req, pageIdx)
		detail := fmt.Sprintf("v%d -> v%d", lock, lock+1)
		if err == nil {
			body := map[string]any{"id": id, "status": "current", "title": title,
				"body":    storageBody(storageOf(req.Local.Root.Child("body"))),
				"version": map[string]any{"number": lock + 1, "message": "gfs"}}
			if parent != "" {
				body["parentId"] = parent
			}
			err = s.c.do(ctx, http.MethodPut, "/wiki/api/v2/pages/"+id, body, nil)
			if errors.Is(err, adapter.ErrLock) {
				return []adapter.Result{{Action: req.Actions[0], Err: err, Code: codeOf(err)}}
			}
			if err == nil {
				ref := s.tree[id]
				ref.Title = title
				if parent != "" {
					ref.Parent = parent
				}
				s.tree[id] = ref
			}
		}
		for _, i := range pageIdx {
			out[i].Err, out[i].Code = err, codeOf(err)
			if err == nil {
				out[i].Detail = detail
			}
		}
	}
	for _, i := range labelIdx {
		var baseLabels []string
		if req.Base != nil {
			baseLabels = labelsOf(req.Base.Root)
		}
		err := s.syncLabels(ctx, id, baseLabels, labelsOf(req.Local.Root))
		out[i].Err, out[i].Code = err, codeOf(err)
	}
	for _, i := range commentIdx {
		err := s.comment(ctx, id, req, req.Actions[i])
		out[i].Err, out[i].Code = err, codeOf(err)
	}
	return out
}

func (s *session) create(ctx context.Context, req adapter.ApplyRequest) []adapter.Result {
	act := req.Actions[0]
	fail := func(err error) []adapter.Result {
		return []adapter.Result{{Action: act, Err: err, Code: codeOf(err)}}
	}
	parent, err := s.parentFor(req.Local.Path, req.IDByPath)
	if err != nil {
		return fail(err)
	}
	title := titleOf(req.Local.Root)
	if title == "" {
		title = baseName(req.Local.Path)
	}
	body := map[string]any{"spaceId": s.spaceID, "status": "current", "title": title,
		"body": storageBody(storageOf(req.Local.Root.Child("body")))}
	if parent != "" {
		body["parentId"] = parent
	}
	var p apiPage
	if err := s.c.do(ctx, http.MethodPost, "/wiki/api/v2/pages", body, &p); err != nil {
		return fail(err)
	}
	s.tree[p.ID] = pageRef{ID: p.ID, Title: p.Title, Parent: p.ParentID}
	out := []adapter.Result{{Action: act, ID: p.ID, Detail: "id=" + p.ID}}
	if ls := labelsOf(req.Local.Root); len(ls) > 0 {
		if err := s.syncLabels(ctx, p.ID, nil, ls); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "update", Group: "labels"}, Err: err, Code: codeOf(err)})
		}
	}
	for n, c := range req.Local.Root.ChildrenNamed("comment") {
		if _, has := c.Attr("id"); has {
			continue
		}
		if err := s.postComment(ctx, p.ID, c); err != nil {
			out = append(out, adapter.Result{Action: adapter.Action{Verb: "create", Target: fmt.Sprintf("comment[%d]", n+1)}, Err: err, Code: codeOf(err)})
		}
	}
	return out
}

func (s *session) syncLabels(ctx context.Context, id string, have, want []string) error {
	hs, ws := map[string]bool{}, map[string]bool{}
	for _, l := range have {
		hs[l] = true
	}
	var add []map[string]string
	for _, l := range want {
		ws[l] = true
		if !hs[l] {
			add = append(add, map[string]string{"prefix": "global", "name": l})
		}
	}
	if len(add) > 0 {
		if err := s.c.do(ctx, http.MethodPost, "/wiki/rest/api/content/"+id+"/label", add, nil); err != nil {
			return err
		}
	}
	for _, l := range have {
		if !ws[l] {
			if err := s.c.do(ctx, http.MethodDelete, "/wiki/rest/api/content/"+id+"/label?name="+url.QueryEscape(l), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *session) postComment(ctx context.Context, pageID string, c *xmltree.Node) error {
	return s.c.do(ctx, http.MethodPost, "/wiki/api/v2/footer-comments",
		map[string]any{"pageId": pageID, "body": storageBody(storageOf(c))}, nil)
}

func (s *session) comment(ctx context.Context, pageID string, req adapter.ApplyRequest, a adapter.Action) error {
	name, id, nth, ok := changes.ParseTarget(a.Target)
	if !ok || name != "comment" {
		return fmt.Errorf("confluence has no sub-resource %q", a.Target)
	}
	switch a.Verb {
	case "create":
		i := 0
		for _, c := range req.Local.Root.ChildrenNamed("comment") {
			if _, has := c.Attr("id"); has {
				continue
			}
			if i++; i == nth {
				return s.postComment(ctx, pageID, c)
			}
		}
		return fmt.Errorf("%s not found in file", a.Target)
	case "update":
		local := validate.FindSub(req.Local.Root, "comment", "id", id)
		base := validate.FindSub(req.Base.Root, "comment", "id", id)
		if local == nil || base == nil {
			return fmt.Errorf("%s not found", a.Target)
		}
		v, _ := base.Attr("version")
		n, _ := strconv.Atoi(v)
		return s.c.do(ctx, http.MethodPut, "/wiki/api/v2/footer-comments/"+id,
			map[string]any{"version": map[string]any{"number": n + 1}, "body": storageBody(storageOf(local))}, nil)
	case "delete":
		return s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/footer-comments/"+id, nil, nil)
	}
	return fmt.Errorf("confluence has no action %q on %s", a.Verb, a.Target)
}
```

Add the blank import to `internal/cli/root.go`:

```go
import (
	// ...
	_ "github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence" // registers confluence://
)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapter/confluence/ -v && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/confluence internal/cli/root.go
git commit -m "confluence: adapter session (list, fetch, apply, check) and registration"
```

---
### Task 20: End-to-end Confluence test, corpus check, docs

Proves the whole stack through the CLI against the fake server, adds the spec's pre-ship corpus check (spec 12, last bullet) as an opt-in test, and updates the README section that describes the deleted spike.

**Files:**
- Create: `internal/cli/confluence_e2e_test.go`, `internal/adapter/confluence/corpus_test.go`
- Modify: `Readme.md` (only the `# Current repo state` section; the file has unrelated uncommitted edits — keep them, stage the whole file only if the user agrees, otherwise leave `Readme.md` out of the commit and tell them)

**Interfaces:**
- Consumes: everything.

- [ ] **Step 1: Write the end-to-end test** `internal/cli/confluence_e2e_test.go`

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter/confluence/cftest"
)

func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q:\n%s", path, old, b)
	}
	os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644)
}

func mustRun(t *testing.T, want int, args ...string) string {
	t.Helper()
	out, code := gfs(t, args...)
	if code != want {
		t.Fatalf("gfs %v: exit %d, want %d\n%s", args, code, want, out)
	}
	return out
}

func TestConfluenceEndToEnd(t *testing.T) {
	srv := cftest.New()
	defer srv.Close()
	srv.AddSpace("ENG", "100")
	srv.AddPage(cftest.Page{ID: "98001", Title: "Home", SpaceID: "100", Storage: "<p>Engineering space.</p>"})
	srv.AddPage(cftest.Page{ID: "98120", Title: "Architecture", ParentID: "98001", SpaceID: "100", Storage: "<p>a</p><p>b</p><p>c</p>"})
	srv.AddPage(cftest.Page{ID: "98130", Title: "Runbooks", ParentID: "98001", SpaceID: "100", Storage: "<p>Ops.</p>"})
	t.Setenv("GFS_CONFLUENCE_TOKEN", "t")
	t.Setenv("GFS_CONFLUENCE_EMAIL", "me@x.com")
	remote := "confluence://acme.atlassian.net/ENG?base=" + srv.URL

	dir := t.TempDir()
	t.Chdir(dir)
	mustRun(t, 0, "clone", remote, "wt")
	t.Chdir(filepath.Join(dir, "wt"))
	arch := "eng/Home/Architecture.xml"
	if _, err := os.Stat(arch); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	// 1. update a page body
	replaceIn(t, arch, "<p>a</p>", "<p>alpha</p>")
	if out := mustRun(t, 0, "commit"); !strings.Contains(out, "v1 -> v2") {
		t.Fatal(out)
	}
	if p, _ := srv.Page("98120"); !strings.Contains(p.Storage, "<p>alpha</p>") || p.Version != 2 {
		t.Fatalf("%+v", p)
	}

	// 2. create a child page from a bare root
	os.MkdirAll("eng/Home/Runbooks", 0o755)
	os.WriteFile("eng/Home/Runbooks/Rollback.xml", []byte(`<page><title>Rollback</title><labels><label>runbook</label></labels>
<body type="application/xhtml+xml"><ol><li>Redeploy previous tag.</li></ol></body></page>`), 0o644)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, `create page under "Runbooks"`) {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit")
	b, _ := os.ReadFile("eng/Home/Runbooks/Rollback.xml")
	if !strings.Contains(string(b), `<page id="`) || !strings.Contains(string(b), `parent="98130"`) {
		t.Fatalf("write-back:\n%s", b)
	}

	// 3. conflict, resolve --theirs, commit is a no-op
	srv.EditPage("98120", func(p *cftest.Page) { p.Storage = strings.Replace(p.Storage, "<p>b</p>", "<p>B remote</p>", 1) })
	replaceIn(t, arch, "<p>b</p>", "<p>B local</p>")
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "conflict with remote v3") {
		t.Fatal(out)
	}
	b, _ = os.ReadFile(arch)
	if !strings.Contains(string(b), "<<<<<<< local") || !strings.Contains(string(b), `<conflict remote-version="3" by="bob"`) {
		t.Fatalf("%s", b)
	}
	mustRun(t, 0, "resolve", "--theirs", arch)
	if out := mustRun(t, 0, "status"); !strings.Contains(out, "nothing to commit") {
		t.Fatal(out)
	}

	// 4. pull a remote comment
	srv.AddComment(cftest.Comment{PageID: "98120", Storage: "<p>Nice.</p>"})
	if out := mustRun(t, 0, "pull"); !strings.Contains(out, "~  "+arch) {
		t.Fatal(out)
	}
	b, _ = os.ReadFile(arch)
	if !strings.Contains(string(b), "<p>Nice.</p>") {
		t.Fatalf("%s", b)
	}

	// 5. delete needs --allow on a non-TTY
	os.Remove("eng/Home/Runbooks/Rollback.xml")
	if out := mustRun(t, 1, "commit"); !strings.Contains(out, "--allow delete") {
		t.Fatal(out)
	}
	mustRun(t, 0, "commit", "--allow", "delete")

	// 6. a fresh clone is byte-identical to the working tree (canonical form is stable)
	t.Chdir(dir)
	mustRun(t, 0, "clone", remote, "wt2")
	for _, p := range []string{"eng/Home.xml", arch, "eng/Home/Runbooks.xml"} {
		a, _ := os.ReadFile(filepath.Join(dir, "wt", p))
		c, _ := os.ReadFile(filepath.Join(dir, "wt2", p))
		if string(a) != string(c) {
			t.Fatalf("%s differs between working tree and fresh clone:\n%s\n---\n%s", p, a, c)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wt2", "eng/Home/Runbooks/Rollback.xml")); err == nil {
		t.Fatal("deleted page came back")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/cli/ -run ConfluenceEndToEnd -v`
Expected: PASS. A failure here is an integration bug in an earlier task: fix it there (with a unit test that reproduces it) rather than special-casing this test.

- [ ] **Step 3: Add the opt-in corpus check** `internal/adapter/confluence/corpus_test.go`

The spec requires, before shipping this adapter: pull real pages, canonicalise, push unchanged, pull again, assert no diff. Pushing bumps page versions, so this runs **only** against a scratch copy of a space and only when `GFS_CORPUS_URL` is set.

```go
package confluence

import (
	"net/url"
	"os"
	"testing"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/canon"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// GFS_CORPUS_URL=confluence://<host>/<SCRATCHSPACE> GFS_CONFLUENCE_EMAIL=... GFS_CONFLUENCE_TOKEN=... \
//   go test ./internal/adapter/confluence/ -run Corpus -v
// WARNING: writes a new version of every page in the space.
func TestCorpusRoundTrip(t *testing.T) {
	raw := os.Getenv("GFS_CORPUS_URL")
	if raw == "" {
		t.Skip("set GFS_CORPUS_URL to a scratch space to run the corpus check")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := parseTarget(u, nil, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(bg, tg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.List(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(r *adapter.Resource) string {
		c := r.Root.Clone()
		c.DelAttr("version")
		c.DelAttr("updated")
		canon.Normalize(c, pageSchema)
		return xmltree.Print(c, 0)
	}
	for _, r := range l.Resources {
		before := canonical(&r)
		res := s.Apply(bg, adapter.ApplyRequest{Local: &r, Base: &r, Lock: r.Version,
			Actions: []adapter.Action{{Verb: "update", Group: "body"}}, IDByPath: func(string) (string, bool) { return "", false }})
		if res[0].Err != nil {
			t.Errorf("%s: push: %v", r.Path, res[0].Err)
			continue
		}
		after, err := s.Fetch(bg, r.ID)
		if err != nil {
			t.Fatalf("%s: fetch: %v", r.Path, err)
		}
		if got := canonical(after); got != before {
			t.Errorf("%s changed after an unchanged push:\n--- before\n%s\n--- after\n%s", r.Path, before, got)
		}
	}
	t.Logf("%d pages round-tripped", len(l.Resources))
}
```

Run: `go test ./internal/adapter/confluence/ -run Corpus -v`
Expected: `SKIP` without `GFS_CORPUS_URL`. Tell the user this must be run once against a scratch space before relying on the adapter; it is not part of CI.

- [ ] **Step 4: Update the README's "Current repo state" section**

Replace the body of `# Current repo state` in `Readme.md` (everything up to the next `# ` heading) with:

````markdown
# Current repo state

The CLI is implemented in Go and follows `docs/superpowers/specs/2026-09-23-gfs-cli-design.md`.
Implemented: the shared core (XML file model, canonical printer, status/diff/commit/pull/resolve/log/actions,
three-way merge, policy) and one adapter, **Confluence Cloud**.

```shell
export GFS_CONFLUENCE_EMAIL=me@example.com GFS_CONFLUENCE_TOKEN=<atlassian api token>
gfs clone confluence://acme.atlassian.net/ENG confluence
cd confluence
vim eng/Home/Architecture.xml
gfs status
gfs commit --dry-run
gfs commit
```

See `example/` for how every adapter's files are meant to look.
````

Do not touch the rest of the file.

- [ ] **Step 5: Full verification**

Run: `gofmt -l . && go vet ./... && go test ./... && go build -o /tmp/gfs ./cmd/gfs && /tmp/gfs --help`
Expected: `gofmt` prints nothing, all tests PASS (corpus test SKIP), help lists `clone status diff commit pull resolve log actions`.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/confluence_e2e_test.go internal/adapter/confluence/corpus_test.go
git commit -m "confluence: end-to-end CLI test and opt-in corpus round-trip check"
# Readme.md: see the Files note above before staging it
```

---

## Self-review notes (for the executor)

- Spec items deliberately **not** implemented here, because they belong to other adapters or are open questions in the spec: `send` verb (mail), flat path model behaviour beyond `IsMove` (Jira), incremental pull cursors (none for Confluence; the index supports them), attachments, credential storage, multi-remote roots, schema publication.
- Known limitations to report after execution: after a parent page is moved or renamed, its children's files move on the next `gfs pull`, not during the commit; comment `created` is the comment's latest version time (API v2 does not expose the original creation time), so an edited comment may re-sort; moving a page to the space root is refused.
