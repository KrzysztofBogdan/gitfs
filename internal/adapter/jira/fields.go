package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type codec int

const (
	cRaw codec = iota
	cText
	cADF
	cNumber
	cDate
	cDateTime
	cOption
	cOptions
	cUser
	cUsers
	cLabels
	cKey
	cSprint
)

// customCodecs maps a custom field type (the part after ':' of schema.custom,
// from the prefixes below) to its file shape (jira spec §5.3).
var customCodecs = map[string]codec{
	"textfield": cText, "url": cText, "gh-epic-label": cText,
	"textarea": cADF,
	"float":    cNumber, "jsw-story-points": cNumber,
	"datepicker": cDate, "datetime": cDateTime,
	"select": cOption, "radiobuttons": cOption, "gh-epic-status": cOption,
	"multiselect": cOptions, "multicheckboxes": cOptions,
	"userpicker": cUser, "multiuserpicker": cUsers, "sd-request-participants": cUsers,
	"labels":       cLabels,
	"gh-epic-link": cKey,
	"gh-sprint":    cSprint,
}

var customPrefixes = map[string]bool{
	"com.atlassian.jira.plugin.system.customfieldtypes": true,
	"com.pyxis.greenhopper.jira":                        true,
	"com.atlassian.servicedesk":                         true,
}

func codecOf(m fieldMeta) codec {
	prefix, name, ok := strings.Cut(m.Custom, ":")
	if !ok || !customPrefixes[prefix] {
		return cRaw
	}
	return customCodecs[name]
}

type apiOption struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func optionNode(o apiOption) *xmltree.Node {
	n := el("option", "id", o.ID)
	if o.Value != "" {
		n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(o.Value)}}
	}
	return n
}

func isEmptyJSON(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "[]", `""`, "{}":
		return true
	}
	return false
}

// rawField shows a value gfs has no codec for, read-only, as JSON.
func rawField(m fieldMeta, raw json.RawMessage) *xmltree.Node {
	var b bytes.Buffer
	json.Compact(&b, raw)
	n := el("field", "id", m.ID, "name", m.Name, "type", "raw")
	n.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: b.String()}}
	return n
}

// decodeField renders a custom (or unmapped system) field value as
// <field id name [type]>; nil for an empty value. A value whose JSON shape
// does not match its codec is shown raw, so pull never fails on it.
func decodeField(m fieldMeta, raw json.RawMessage, reg *registry) (*xmltree.Node, error) {
	if isEmptyJSON(raw) {
		return nil, nil
	}
	n := el("field", "id", m.ID, "name", m.Name)
	add := func(c *xmltree.Node) { n.Children = append(n.Children, c) }
	switch codecOf(m) {
	case cText, cDate, cDateTime, cKey:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return rawField(m, raw), nil
		}
		add(&xmltree.Node{Kind: xmltree.Text, Text: xmlText(s)})
	case cNumber:
		var f json.Number
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&f) != nil {
			return rawField(m, raw), nil
		}
		add(&xmltree.Node{Kind: xmltree.Text, Text: f.String()})
	case cADF:
		kids, err := adfToNodes(raw)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", m.ID, err)
		}
		n.SetAttr("type", "adf")
		n.Children = kids
		reg.seeMentions(kids)
	case cOption:
		var o apiOption
		if json.Unmarshal(raw, &o) != nil {
			return rawField(m, raw), nil
		}
		add(optionNode(o))
	case cOptions:
		var os []apiOption
		if json.Unmarshal(raw, &os) != nil {
			return rawField(m, raw), nil
		}
		for _, o := range os {
			add(optionNode(o))
		}
	case cUser:
		var u apiUser
		if json.Unmarshal(raw, &u) != nil || u.AccountID == "" {
			return rawField(m, raw), nil
		}
		reg.see(u)
		add(userNode("user", u))
	case cUsers:
		var us []apiUser
		if json.Unmarshal(raw, &us) != nil {
			return rawField(m, raw), nil
		}
		for _, u := range us {
			reg.see(u)
			add(userNode("user", u))
		}
	case cLabels:
		var ls []string
		if json.Unmarshal(raw, &ls) != nil {
			return rawField(m, raw), nil
		}
		for _, l := range ls {
			add(textEl("label", l))
		}
	case cSprint:
		var ss []struct {
			ID   json.Number `json:"id"`
			Name string      `json:"name"`
		}
		if json.Unmarshal(raw, &ss) != nil {
			return rawField(m, raw), nil
		}
		for _, s := range ss {
			sp := el("sprint", "id", s.ID.String())
			sp.Children = []*xmltree.Node{{Kind: xmltree.Text, Text: xmlText(s.Name)}}
			add(sp)
		}
	default:
		return rawField(m, raw), nil
	}
	return n, nil
}
