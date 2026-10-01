package dnsx

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ScratchZone returns the zone a real-site write check may change, and the
// backup directory to fill before it does (DNS spec §10.3). The zone must
// be named twice: in zoneVar and in GFS_DNS_WRITE_CONFIRM; the backup
// directory comes from GFS_DNS_BACKUP_DIR. skip is the reason to skip.
func ScratchZone(provider, zoneVar string, getenv func(string) string, now time.Time) (zone, backup, skip string) {
	zone = getenv(zoneVar)
	switch {
	case zone == "":
		return "", "", zoneVar + " not set"
	case getenv("GFS_DNS_WRITE_CONFIRM") != zone:
		return "", "", "GFS_DNS_WRITE_CONFIRM must repeat " + zoneVar + " (" + zone + ")"
	case getenv("GFS_DNS_BACKUP_DIR") == "":
		return "", "", "GFS_DNS_BACKUP_DIR not set: no backup, no writes"
	}
	dir := filepath.Join(getenv("GFS_DNS_BACKUP_DIR"), fmt.Sprintf("%s-%s-%s", provider, zone, now.UTC().Format("20060102T150405Z")))
	return zone, dir, ""
}

// WriteBackup writes files (name -> content) into dir, creating it.
func WriteBackup(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
