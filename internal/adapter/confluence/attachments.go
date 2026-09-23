package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
	"github.com/KrzysztofBogdan/gitfs/internal/xmltree"
)

type apiAttachment struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	MediaType    string     `json:"mediaType"`
	FileSize     int64      `json:"fileSize"`
	DownloadLink string     `json:"downloadLink"`
	Version      apiVersion `json:"version"`
}

// v1Attachment is the attachment shape of the REST v1 upload responses.
type v1Attachment struct {
	ID      string `json:"id"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
}

// attachmentNodes renders a page's attachments as <attachment> elements (attachments spec 7).
func attachmentNodes(atts []apiAttachment, name func(string) string) []*xmltree.Node {
	var out []*xmltree.Node
	for _, a := range atts {
		author := name(a.Version.AuthorID)
		if author == "" {
			author = a.Version.AuthorID
		}
		out = append(out, el("attachment", "id", a.ID, "name", a.Title, "type", a.MediaType,
			"size", strconv.FormatInt(a.FileSize, 10), "version", strconv.Itoa(a.Version.Number),
			"created", a.Version.CreatedAt, "author", author))
	}
	return out
}

func (s *session) listAttachments(ctx context.Context, pageID string) ([]apiAttachment, error) {
	var out []apiAttachment
	err := s.c.paginate(ctx, "/wiki/api/v2/pages/"+pageID+"/attachments?limit=250", func(raw json.RawMessage) error {
		var a apiAttachment
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

// Download streams an attachment's current bytes: its metadata names the
// download link, which Confluence redirects to the media store.
func (s *session) Download(ctx context.Context, _ string, attID string, w io.Writer) (adapter.AttachmentInfo, error) {
	var a apiAttachment
	if err := s.c.do(ctx, http.MethodGet, "/wiki/api/v2/attachments/"+url.PathEscape(attID), nil, &a); err != nil {
		return adapter.AttachmentInfo{}, err
	}
	if a.DownloadLink == "" {
		return adapter.AttachmentInfo{}, fmt.Errorf("confluence: attachment %s has no download link", attID)
	}
	n, err := s.c.download(ctx, "/wiki"+a.DownloadLink, w)
	return adapter.AttachmentInfo{Version: strconv.Itoa(a.Version.Number), Size: n}, err
}

// attachment executes one attachment action on page pageID. Uploads carry no
// version lock; commit checks the remote version just before (spec 4.4, 7).
func (s *session) attachment(ctx context.Context, pageID string, req adapter.ApplyRequest, a adapter.Action) (id, version string, err error) {
	attID, _, ok := adapter.ParseAttachmentTarget(a.Target)
	if !ok {
		return "", "", fmt.Errorf("confluence has no attachment target %q", a.Target)
	}
	upload := func(apiPath string, out any) error {
		if req.Open == nil {
			return errors.New("confluence: no file reader for uploads")
		}
		f, err := req.Open(a.File)
		if err != nil {
			return err
		}
		defer f.Close()
		return s.c.upload(ctx, apiPath, path.Base(a.File), f, out)
	}
	switch a.Verb {
	case "create":
		var resp struct {
			Results []v1Attachment `json:"results"`
		}
		if err := upload("/wiki/rest/api/content/"+pageID+"/child/attachment", &resp); err != nil {
			return "", "", err
		}
		if len(resp.Results) == 0 {
			return "", "", errors.New("confluence: upload returned no attachment")
		}
		return resp.Results[0].ID, strconv.Itoa(resp.Results[0].Version.Number), nil
	case "update":
		var resp v1Attachment
		if err := upload("/wiki/rest/api/content/"+pageID+"/child/attachment/"+attID+"/data", &resp); err != nil {
			return "", "", err
		}
		return attID, strconv.Itoa(resp.Version.Number), nil
	case "delete":
		return attID, "", s.c.do(ctx, http.MethodDelete, "/wiki/api/v2/attachments/"+url.PathEscape(attID), nil, nil)
	}
	return "", "", fmt.Errorf("confluence has no action %q on %s", a.Verb, a.Target)
}

// checkAttachment is the dry-run check of one attachment action: Confluence
// refuses a second attachment with the same name on a page.
func checkAttachment(req adapter.ApplyRequest, a adapter.Action) error {
	if a.Verb != "create" || req.Local == nil || req.Local.Root == nil {
		return nil
	}
	_, file, _ := adapter.ParseAttachmentTarget(a.Target)
	for _, c := range req.Local.Root.ChildrenNamed("attachment") {
		if n, _ := c.Attr("name"); n == file {
			return fmt.Errorf("an attachment named %q already exists on this page; edit its file instead", file)
		}
	}
	return nil
}
