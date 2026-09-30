package jtest

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
)

func init() { portalRegistrars = append(portalRegistrars, (*Portal).writeRoutes) }

func (p *Portal) writeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /rest/servicedeskapi/request/{id}/transition", p.getTransitions)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/transition", p.doTransition)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/participant", p.participants(true))
	mux.HandleFunc("DELETE /rest/servicedeskapi/request/{id}/participant", p.participants(false))
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/comment", p.addComment)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/approval/{aid}", p.answer)
	mux.HandleFunc("POST /rest/servicedeskapi/servicedesk/{desk}/attachTemporaryFile", p.tempFile)
	mux.HandleFunc("POST /rest/servicedeskapi/request/{id}/attachment", p.attach)
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk/{desk}/requesttype", p.getTypes)
	mux.HandleFunc("GET /rest/servicedeskapi/servicedesk/{desk}/requesttype/{t}/field", p.getTypeFields)
	mux.HandleFunc("POST /rest/servicedeskapi/request", p.raise)
}

func (p *Portal) getTransitions(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, t := range x.Transitions {
		out = append(out, map[string]any{"id": t.ID, "name": t.Name})
	}
	writeJSON(w, pageOf(out, 0, 50))
}

func (p *Portal) doTransition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	for _, t := range x.Transitions {
		if t.ID == body.ID {
			x.Status, x.Category, x.StatusDate = t.To, t.Category, p.now()
			x.History++
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	jiraError(w, 400, "The transition is not valid for this request.", nil)
}

func (p *Portal) participants(add bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AccountIDs []string `json:"accountIds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		defer p.mu.Unlock()
		x := p.find(w, r.PathValue("id"))
		if x == nil {
			return
		}
		for _, a := range body.AccountIDs {
			switch i := slices.Index(x.Participants, a); {
			case add && i < 0:
				x.Participants = append(x.Participants, a)
			case !add && i >= 0:
				x.Participants = slices.Delete(x.Participants, i, i+1)
			}
		}
		writeJSON(w, map[string]any{"values": []any{}})
	}
}

func (p *Portal) addComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body   string `json:"body"`
		Public bool   `json:"public"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	if !body.Public {
		jiraError(w, 400, "Customers can only add public comments.", nil)
		return
	}
	c := &CustomerComment{ID: p.nextID(), Author: "me", Body: body.Body, Public: true, Created: p.now()}
	x.Comments = append(x.Comments, c)
	writeJSON(w, map[string]any{"id": c.ID})
}

func (p *Portal) answer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision string `json:"decision"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	for _, a := range x.Approvals {
		if a.ID == r.PathValue("aid") {
			if !a.CanAnswer || a.Decision != "pending" {
				jiraError(w, 403, "You cannot answer this approval.", nil)
				return
			}
			a.Decision = map[string]string{"approve": "approved", "decline": "declined"}[body.Decision]
			writeJSON(w, map[string]any{"id": a.ID, "finalDecision": a.Decision})
			return
		}
	}
	jiraError(w, 404, "no approval", nil)
}

func (p *Portal) tempFile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Atlassian-Token") != "no-check" || r.Header.Get("X-ExperimentalApi") != "opt-in" {
		jiraError(w, 403, "missing X-Atlassian-Token or X-ExperimentalApi", nil)
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		jiraError(w, 400, err.Error(), nil)
		return
	}
	data, _ := io.ReadAll(f)
	p.mu.Lock()
	defer p.mu.Unlock()
	id := "temp-" + p.nextID()
	p.temps[id] = &Attachment{Filename: h.Filename, Mime: h.Header.Get("Content-Type"), Data: data}
	writeJSON(w, map[string]any{"temporaryAttachments": []any{map[string]any{"temporaryAttachmentId": id, "fileName": h.Filename}}})
}

func (p *Portal) attach(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string `json:"temporaryAttachmentIds"`
		Public bool     `json:"public"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	x := p.find(w, r.PathValue("id"))
	if x == nil {
		return
	}
	var out []any
	for _, id := range body.IDs {
		t := p.temps[id]
		if t == nil {
			jiraError(w, 400, "unknown temporary attachment "+id, nil)
			return
		}
		a := &Attachment{ID: p.nextID(), Filename: t.Filename, Mime: t.Mime, Data: t.Data, Author: "me", Created: p.now()}
		x.Attachments = append(x.Attachments, a)
		out = append(out, p.attachmentJSON(a))
	}
	writeJSON(w, map[string]any{"attachments": map[string]any{"values": out}})
}

func (p *Portal) getTypes(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, t := range p.types[r.PathValue("desk")] {
		out = append(out, map[string]any{"id": t.ID, "name": t.Name})
	}
	writeJSON(w, pageOf(out, 0, 50))
}

func (p *Portal) getTypeFields(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, f := range p.fields[r.PathValue("desk")+"/"+r.PathValue("t")] {
		out = append(out, map[string]any{"fieldId": f.ID, "name": f.Name, "required": f.Required})
	}
	writeJSON(w, map[string]any{"requestTypeFields": out})
}

func (p *Portal) raise(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceDeskID      string         `json:"serviceDeskId"`
		RequestTypeID      string         `json:"requestTypeId"`
		RequestFieldValues map[string]any `json:"requestFieldValues"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := map[string]string{}
	allowed := map[string]bool{}
	for _, f := range p.fields[body.ServiceDeskID+"/"+body.RequestTypeID] {
		allowed[f.ID] = true
		if _, ok := body.RequestFieldValues[f.ID]; f.Required && !ok {
			errs[f.ID] = f.Name + " is required."
		}
	}
	for id := range body.RequestFieldValues {
		if !allowed[id] {
			errs[id] = "Field '" + id + "' is not on the request type."
		}
	}
	if len(errs) > 0 {
		jiraError(w, 400, "", errs)
		return
	}
	x := Request{Desk: body.ServiceDeskID, Type: body.RequestTypeID, Reporter: "me", Mine: true, Fields: map[string]any{}}
	for id, v := range body.RequestFieldValues {
		switch id {
		case "summary":
			x.Summary, _ = v.(string)
		case "description":
			x.Description, _ = v.(string)
		default:
			x.Fields[id] = v
		}
	}
	nx := p.addRequest(x)
	writeJSON(w, map[string]any{"issueId": nx.ID, "issueKey": nx.Key})
}
