package atlassian

import (
	"context"
	"net/http"
	"strings"
)

// VerifyToken checks email and token against the site at base and returns
// the account's display name. It tries Jira, then Confluence, then the
// service desk customer API (customers see neither product API), so a token
// verifies on any Atlassian site the account can use. When every check fails
// the error of an auth failure (401, 403) wins over a missing product (404).
func VerifyToken(ctx context.Context, base, email, token string) (string, error) {
	c := New(Target{Base: strings.TrimRight(base, "/"), Email: email, Token: token}, "Atlassian")
	var first, auth error
	note := func(err error) {
		if first == nil {
			first = err
		}
		if code := Code(err); auth == nil && (code == "401" || code == "403") {
			auth = err
		}
	}
	for _, p := range []string{"/rest/api/3/myself", "/wiki/rest/api/user/current"} {
		var u struct {
			DisplayName string `json:"displayName"`
		}
		err := c.Do(ctx, http.MethodGet, p, nil, &u)
		if err == nil && u.DisplayName != "" {
			return u.DisplayName, nil
		}
		note(err)
	}
	err := c.Do(ctx, http.MethodGet, "/rest/servicedeskapi/request?limit=1", nil, nil)
	if err == nil {
		return email, nil
	}
	note(err)
	if auth != nil {
		return "", auth
	}
	return "", first
}
