package bitbucket

import (
	"strings"
	"time"
)

// Bitbucket Cloud 2.0 JSON (bitbucket spec §5, §6), only the fields gfs reads.

type apiUser struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
	UUID        string `json:"uuid"`
}

type apiBranch struct {
	Name                 string   `json:"name"`
	MergeStrategies      []string `json:"merge_strategies"`
	DefaultMergeStrategy string   `json:"default_merge_strategy"`
}

type apiCommit struct {
	Hash string `json:"hash"`
}

type apiEndpoint struct {
	Branch apiBranch  `json:"branch"`
	Commit *apiCommit `json:"commit"`
}

type apiParticipant struct {
	User     apiUser `json:"user"`
	Role     string  `json:"role"`
	Approved bool    `json:"approved"`
	State    *string `json:"state"`
}

type apiPR struct {
	ID                int              `json:"id"`
	Title             string           `json:"title"`
	Description       string           `json:"description"`
	State             string           `json:"state"`
	Draft             bool             `json:"draft"`
	Author            apiUser          `json:"author"`
	Source            apiEndpoint      `json:"source"`
	Destination       apiEndpoint      `json:"destination"`
	MergeCommit       *apiCommit       `json:"merge_commit"`
	CloseSourceBranch bool             `json:"close_source_branch"`
	Reviewers         []apiUser        `json:"reviewers"`
	Participants      []apiParticipant `json:"participants"`
	CreatedOn         string           `json:"created_on"`
	UpdatedOn         string           `json:"updated_on"`
}

type apiComment struct {
	ID      int `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User   apiUser `json:"user"`
	Parent *struct {
		ID int `json:"id"`
	} `json:"parent"`
	Inline *struct {
		Path string `json:"path"`
		From *int   `json:"from"`
		To   *int   `json:"to"`
	} `json:"inline"`
	Deleted    bool      `json:"deleted"`
	Resolution *struct{} `json:"resolution"`
	CreatedOn  string    `json:"created_on"`
	UpdatedOn  string    `json:"updated_on"`
}

type apiStatus struct {
	Key   string `json:"key"`
	State string `json:"state"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

type apiState struct {
	Name  string `json:"name"`
	Stage *struct {
		Name string `json:"name"`
	} `json:"stage"`
	Result *struct {
		Name string `json:"name"`
	} `json:"result"`
}

type apiPipeline struct {
	UUID        string   `json:"uuid"`
	BuildNumber int      `json:"build_number"`
	Creator     apiUser  `json:"creator"`
	State       apiState `json:"state"`
	Target      struct {
		Type     string    `json:"type"`
		RefType  string    `json:"ref_type"`
		RefName  string    `json:"ref_name"`
		Source   string    `json:"source"`
		Commit   apiCommit `json:"commit"`
		Selector struct {
			Type    string `json:"type"`
			Pattern string `json:"pattern"`
		} `json:"selector"`
		PullRequest *struct {
			ID int `json:"id"`
		} `json:"pullrequest"`
	} `json:"target"`
	Trigger struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"trigger"`
	CreatedOn         string `json:"created_on"`
	CompletedOn       string `json:"completed_on"`
	DurationInSeconds int    `json:"duration_in_seconds"`
	BuildSecondsUsed  int    `json:"build_seconds_used"`
}

type apiStep struct {
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Image *struct {
		Name string `json:"name"`
	} `json:"image"`
	State             apiState `json:"state"`
	StartedOn         string   `json:"started_on"`
	CompletedOn       string   `json:"completed_on"`
	DurationInSeconds int      `json:"duration_in_seconds"`
}

// page is one page of a paged listing.
type page[T any] struct {
	Next   string `json:"next"`
	Values []T    `json:"values"`
}

// stamp is a Bitbucket time as RFC 3339 UTC seconds; "" stays "".
func stamp(v string) string {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return v
	}
	return t.UTC().Format(time.RFC3339)
}

func parseStamp(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

// unbrace drops the braces of a Bitbucket uuid.
func unbrace(uuid string) string { return strings.Trim(uuid, "{}") }
