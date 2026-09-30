package jira

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

const (
	workflowsID   = "workflows"
	workflowsPath = ".workflows.xml"
)

// workflows is the .workflows.xml resource (jira spec §5.9), rebuilt when
// asked, when metadata changed, or when none was built or cached yet.
func (s *session) workflows(ctx context.Context, rebuild bool) (adapter.Resource, error) {
	if s.wf == nil || rebuild || s.wfStale {
		root, err := s.buildWorkflows(ctx)
		if err != nil {
			return adapter.Resource{}, fmt.Errorf("workflows: %w", err)
		}
		s.wf, s.wfStale, s.wfDirty = root, false, true
	}
	return adapter.Resource{ID: workflowsID, Version: contentHash(s.wf), Path: workflowsPath, Root: s.wf}, nil
}

func (s *session) buildWorkflows(ctx context.Context) (*xmltree.Node, error) {
	for _, p := range s.sorted() {
		if p.Types == nil {
			if err := s.loadTypes(ctx, p); err != nil {
				return nil, err
			}
		}
	}
	var perm struct {
		Permissions map[string]struct {
			HavePermission bool `json:"havePermission"`
		} `json:"permissions"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/mypermissions?permissions=ADMINISTER", nil, &perm); err != nil {
		return nil, err
	}
	if perm.Permissions["ADMINISTER"].HavePermission {
		return s.adminWorkflows(ctx)
	}
	return s.statusWorkflows(ctx)
}

type scheme struct{ project, typ string }

func typeNames(p *projectMeta) []*typeMeta {
	ts := make([]*typeMeta, 0, len(p.Types))
	for _, t := range p.Types {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return ts
}

// adminWorkflows reads the full graphs: workflow schemes map (project, type)
// to a workflow, and each workflow lists its statuses and transitions.
func (s *session) adminWorkflows(ctx context.Context) (*xmltree.Node, error) {
	var sts []struct {
		ID, Name       string
		StatusCategory struct{ Key string } `json:"statusCategory"`
	}
	if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/status", nil, &sts); err != nil {
		return nil, err
	}
	category, statusName := map[string]string{}, map[string]string{}
	for _, st := range sts {
		category[st.ID], statusName[st.ID] = st.StatusCategory.Key, st.Name
	}
	uses := map[string][]scheme{}
	for _, p := range s.sorted() {
		var resp struct {
			Values []struct {
				WorkflowScheme struct {
					DefaultWorkflow   string            `json:"defaultWorkflow"`
					IssueTypeMappings map[string]string `json:"issueTypeMappings"`
				} `json:"workflowScheme"`
			} `json:"values"`
		}
		if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/workflowscheme/project?projectId="+url.QueryEscape(p.ID), nil, &resp); err != nil {
			return nil, err
		}
		if len(resp.Values) == 0 {
			continue
		}
		ws := resp.Values[0].WorkflowScheme
		for _, t := range typeNames(p) {
			name := ws.IssueTypeMappings[t.ID]
			if name == "" {
				name = ws.DefaultWorkflow
			}
			uses[name] = append(uses[name], scheme{p.Key, t.Name})
		}
	}
	names := make([]string, 0, len(uses))
	for n := range uses {
		names = append(names, n)
	}
	sort.Strings(names)
	root := el("workflows", "id", workflowsID)
	for _, name := range names {
		var resp struct {
			Values []struct {
				Statuses []struct {
					ID, Name string
				} `json:"statuses"`
				Transitions []struct {
					Name string   `json:"name"`
					From []string `json:"from"`
					To   string   `json:"to"`
					Type string   `json:"type"`
				} `json:"transitions"`
			} `json:"values"`
		}
		path := "/rest/api/3/workflow/search?expand=transitions,statuses&workflowName=" + url.QueryEscape(name)
		if err := s.c.Do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		w := el("workflow", "name", name)
		for _, u := range uses[name] {
			w.Children = append(w.Children, el("scheme", "project", u.project, "type", u.typ))
		}
		if len(resp.Values) > 0 {
			v := resp.Values[0]
			for _, st := range v.Statuses {
				statusName[st.ID] = st.Name
				w.Children = append(w.Children, el("status", "name", st.Name, "category", category[st.ID]))
			}
			var ts []*xmltree.Node
			for _, t := range v.Transitions {
				if t.Type == "initial" {
					continue
				}
				n := el("transition", "name", t.Name, "to", statusName[t.To])
				var from []string
				for _, f := range t.From {
					from = append(from, statusName[f])
				}
				sort.Strings(from)
				for _, f := range from {
					n.Children = append(n.Children, textEl("from", f))
				}
				ts = append(ts, n)
			}
			sort.SliceStable(ts, func(i, j int) bool {
				a, _ := ts[i].Attr("name")
				b, _ := ts[j].Attr("name")
				return a < b
			})
			w.Children = append(w.Children, ts...)
		}
		root.Children = append(root.Children, w)
	}
	return root, nil
}

// statusWorkflows is what any user may read: statuses per issue type.
func (s *session) statusWorkflows(ctx context.Context) (*xmltree.Node, error) {
	root := el("workflows", "id", workflowsID)
	root.Children = append(root.Children, textEl("note",
		"transitions need the Administer Jira permission; run gfs actions with an issue file to see what you can do to it"))
	for _, p := range s.sorted() {
		var types []struct {
			ID, Name string
			Statuses []struct {
				Name           string
				StatusCategory struct{ Key string } `json:"statusCategory"`
			} `json:"statuses"`
		}
		if err := s.c.Do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(p.Key)+"/statuses", nil, &types); err != nil {
			return nil, err
		}
		sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
		for _, t := range types {
			w := el("workflow", "name", p.Key+" "+t.Name)
			w.Children = append(w.Children, el("scheme", "project", p.Key, "type", t.Name))
			for _, st := range t.Statuses {
				w.Children = append(w.Children, el("status", "name", st.Name, "category", st.StatusCategory.Key))
			}
			root.Children = append(root.Children, w)
		}
	}
	return root, nil
}
