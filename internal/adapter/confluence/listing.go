package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KrzysztofBogdan/gitfs/internal/adapter"
)

// searchOverlap widens every change search, in minutes, for clock skew and
// search indexing delay (site clone spec §5.3).
const searchOverlap = 10

// List reports every page of the selected spaces as a stub (no body), except
// pages whose comments or attachments changed since the cursor's listing,
// which come in full. The engine downloads the stubs it needs, and reports
// that progress for every adapter (site clone spec §5).
func (s *session) List(ctx context.Context, cursor string) (adapter.Listing, error) {
	start := s.now()
	defer s.report(adapter.Progress{Phase: "done"})
	if err := s.loadAll(ctx); err != nil {
		return adapter.Listing{}, err
	}
	since, incremental := parseCursor(cursor)
	var dirty map[string]bool
	if incremental {
		var err error
		if dirty, err = s.changedContainers(ctx, searchWindow(since, start)); err != nil {
			return adapter.Listing{}, fmt.Errorf("%w (gfs pull --full lists everything without searching)", err)
		}
	}
	ids := make([]string, 0, len(s.tree))
	for id := range s.tree {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	pathsBySpace := map[string]map[string]string{}
	pathOf := func(ref pageRef) string {
		ps, ok := pathsBySpace[ref.Space]
		if !ok {
			ps = s.paths(s.byID[ref.Space])
			pathsBySpace[ref.Space] = ps
		}
		return ps[ref.ID]
	}
	l := adapter.Listing{Full: true, Cursor: start.UTC().Format(time.RFC3339)}
	for _, id := range ids {
		ref := s.tree[id]
		if !dirty[id] {
			l.Resources = append(l.Resources, adapter.Resource{ID: id, Version: strconv.Itoa(ref.Version), Path: pathOf(ref)})
			continue
		}
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

// parseCursor reads List's cursor; anything unreadable means list everything.
func parseCursor(c string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, c)
	return t, err == nil
}

// searchWindow is how many minutes back the change searches reach: the time
// since the last listing, rounded up, plus searchOverlap. CQL reads absolute
// dates in the account's time zone, so gfs only sends relative ones.
func searchWindow(since, now time.Time) int {
	m := int(math.Ceil(now.Sub(since).Minutes()))
	return max(m, 0) + searchOverlap
}

// changedContainers returns the loaded pages whose comments or attachments
// changed in the last mins minutes.
func (s *session) changedContainers(ctx context.Context, mins int) (map[string]bool, error) {
	scope := ""
	if len(s.sel.keys) > 0 {
		q := make([]string, len(s.sel.keys))
		for i, k := range s.sel.keys {
			q[i] = strconv.Quote(k)
		}
		scope = " AND space in (" + strings.Join(q, ",") + ")"
	}
	out := map[string]bool{}
	for _, typ := range []string{"comment", "attachment"} {
		cql := fmt.Sprintf(`type = %s AND lastmodified >= now("-%dm")%s`, typ, mins, scope)
		err := s.c.paginate(ctx, "/wiki/rest/api/content/search?limit=250&expand=container&cql="+url.QueryEscape(cql), func(raw json.RawMessage) error {
			var hit struct {
				Container struct {
					ID string `json:"id"`
				} `json:"container"`
			}
			if err := json.Unmarshal(raw, &hit); err != nil {
				return err
			}
			if _, ok := s.tree[hit.Container.ID]; ok {
				out[hit.Container.ID] = true
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("search %ss changed since the last pull: %w", typ, err)
		}
	}
	return out, nil
}
