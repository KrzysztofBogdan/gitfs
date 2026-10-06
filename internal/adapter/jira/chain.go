package jira

import (
	"errors"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

// step is one <transition> of a <transitions> block (jira chain spec §2):
// its target status, optional transition name, and field elements.
type step struct {
	to, name string
	node     *xmltree.Node
}

// chainOf reads the <transitions> block of an issue file and checks it
// against <status>; nil without a block.
func chainOf(local *xmltree.Node) ([]step, error) {
	block := child(local, "transitions")
	if block == nil {
		return nil, nil
	}
	var steps []step
	seen := map[string]bool{}
	for _, n := range block.ChildrenNamed("transition") {
		to, _ := attr(n, "to")
		to = strings.TrimSpace(to)
		if to == "" {
			return nil, errors.New(`<transition> needs to="<status>"`)
		}
		if seen[strings.ToLower(to)] {
			return nil, fmt.Errorf("%q appears twice in <transitions>; split the round trip over two commits", to)
		}
		seen[strings.ToLower(to)] = true
		for _, c := range n.Children {
			switch {
			case c.Kind != xmltree.Element:
			case c.Name == "comment":
				return nil, errors.New("<comment> cannot go in a <transition>; add it to the issue")
			case c.Name != "field" && elemFields[c.Name] == "":
				return nil, fmt.Errorf("<transition to=%q>: <%s> is not an issue field", to, c.Name)
			}
		}
		name, _ := attr(n, "name")
		steps = append(steps, step{to: to, name: strings.TrimSpace(name), node: n})
	}
	if len(steps) == 0 {
		return nil, errors.New(`<transitions> needs at least one <transition to="…">`)
	}
	last := steps[len(steps)-1]
	if st := textOf(child(local, "status")); !strings.EqualFold(st, last.to) {
		return nil, fmt.Errorf("<status> %s does not match the last <transition to=%q>", st, last.to)
	}
	return steps, nil
}

// resumeAt is the first step to run when the issue is at status current:
// the one after the step that leads there, else the first (§4.1).
func resumeAt(steps []step, current string) int {
	for i, st := range steps {
		if strings.EqualFold(st.to, current) {
			return i + 1
		}
	}
	return 0
}

// describeChain is the status action's detail for a file with a block.
func describeChain(root *xmltree.Node) string {
	var via []string
	for _, n := range child(root, "transitions").ChildrenNamed("transition") {
		to, _ := attr(n, "to")
		via = append(via, to)
	}
	if len(via) < 2 {
		return "transition to " + textOf(child(root, "status"))
	}
	return "transition via " + strings.Join(via[:len(via)-1], ", ") + " to " + via[len(via)-1]
}
