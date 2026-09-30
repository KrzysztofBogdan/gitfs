package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// attachment runs one attachment action on issue issueID (jira spec §7.6):
// upload a new file or delete one. Jira attachments have no versions, so the
// schema allows no update and the engine never asks for one.
func (s *session) attachment(ctx context.Context, issueID string, req adapter.ApplyRequest, a adapter.Action) (string, error) {
	attID, _, ok := adapter.ParseAttachmentTarget(a.Target)
	if !ok {
		return "", fmt.Errorf("jira has no attachment target %q", a.Target)
	}
	switch a.Verb {
	case "create":
		if req.Open == nil {
			return "", errors.New("jira: no file reader for uploads")
		}
		f, err := req.Open(a.File)
		if err != nil {
			return "", err
		}
		defer f.Close()
		var resp []struct {
			ID string `json:"id"`
		}
		if err := s.c.Upload(ctx, "/rest/api/3/issue/"+url.PathEscape(issueID)+"/attachments", path.Base(a.File), f, nil, &resp); err != nil {
			return "", err
		}
		if len(resp) == 0 {
			return "", errors.New("jira: upload returned no attachment")
		}
		return resp[0].ID, nil
	case "delete":
		return attID, s.c.Do(ctx, http.MethodDelete, "/rest/api/3/attachment/"+url.PathEscape(attID), nil, nil)
	}
	return "", fmt.Errorf("jira cannot %s attachments; delete the file and add it again", a.Verb)
}
