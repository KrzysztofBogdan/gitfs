// Package gitfs holds the documentation built into the gfs binary.
package gitfs

import "embed"

// Docs holds start.md and the Confluence and Jira references, served by gfs help,
// gfs example and gfs schema.
//
//go:embed start.md docs/jira.md docs/confluence.md docs/confluence/storage.md docs/confluence/roundtrip.json docs/confluence/examples
var Docs embed.FS
