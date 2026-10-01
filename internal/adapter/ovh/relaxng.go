package ovh

import "github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"

// RelaxNG is the grammar of OVH zone files (gfs schema ovh).
func RelaxNG() string { return dnsx.RelaxNG(zoneSchema, "OVH", nil) }
