// Package policy decides whether an action class may run (spec 5.3).
package policy

import "fmt"

const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

var defaults = map[string]string{"send": Ask, "delete": Ask, "publish": Ask, "reply": Ask, "approve": Ask, "dns": Ask}

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
	Allowed map[string]bool            // --allow <class>
	Force   bool                       // --force: every ask is allowed
	Prompt  func(question string) bool // nil: not a TTY
}

// Asks reports whether class needs a confirmation in this run.
func (d *Decider) Asks(class string) bool {
	return d.Policy.Level(class) == Ask && !d.Allowed[class] && !d.Force
}

func (d *Decider) Decide(class, question string) (bool, string) {
	switch d.Policy.Level(class) {
	case Deny:
		return false, "denied by policy"
	case Ask:
		if !d.Asks(class) {
			return true, ""
		}
		if d.Prompt == nil {
			return false, "needs confirmation: rerun with --allow " + class + " or --force"
		}
		if !d.Prompt(question) {
			return false, "declined"
		}
	}
	return true, ""
}
