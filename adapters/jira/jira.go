// Package jira is the built-in Jira Cloud adapter.
package jira

import "github.com/KrzysztofBogdan/gitfs/adapter"

func init() {
	adapter.Register("jira", newAdapter)
}
