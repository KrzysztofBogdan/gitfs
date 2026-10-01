package cloudns

import "github.com/KrzysztofBogdan/gitfs/internal/adapter/dnsx"

// RelaxNG is the grammar of ClouDNS zone files and .geodns.xml (gfs schema
// cloudns).
func RelaxNG() string {
	return dnsx.RelaxNG(zoneSchema, "ClouDNS", map[string]string{"dnssec": "ds", "failover": "backup"}, "geodns")
}
