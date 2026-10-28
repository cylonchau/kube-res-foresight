// Package timezone is the one place that says which zone the platform shows and logs time in: UTC+8 unless the deployment names another.
//
// What is stored does not change with it. SQLite keeps every time as text in UTC (see the store), the Prometheus API speaks Unix seconds, and
// the API answers times with their offset; the zone is applied where a person reads: the log lines, the times typed into a filter without an
// offset, the weekly periods of a module that name no zone of their own, and, in the web console, every time shown. So a time is converted
// once, on its way to the eye, and never on its way into the store.
package timezone

import (
	"os"
	"strings"
	"time"
	// the zone database is compiled in, so the zone is the same on a host or in an image that has none
	_ "time/tzdata"
)

// Name is the IANA name of the default zone, UTC+8 all year (no daylight saving).
const Name = "Asia/Shanghai"

// Env names the variable that replaces the default zone, for a deployment elsewhere.
const Env = "KUBE_RESOURCE_FCST_TIMEZONE"

// Offset is the offset of the default zone.
const Offset = 8 * time.Hour

// Location is the zone times are shown in: the one named by KUBE_RESOURCE_FCST_TIMEZONE when it is a zone, else UTC+8.
func Location() *time.Location {
	if name := strings.TrimSpace(os.Getenv(Env)); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(Name); err == nil {
		return loc
	}
	return time.FixedZone("UTC+8", int(Offset/time.Second))
}

// NameOf is the name of Location, for a database connection that is told its zone.
func NameOf() string {
	if loc := Location(); loc != nil {
		return loc.String()
	}
	return Name
}

// Apply makes the zone the local zone of the process: log lines, time.Now() read for a person, and a time parsed without an offset all
// follow it. Call it first thing in main, before anything reads the clock.
func Apply() *time.Location {
	loc := Location()
	time.Local = loc
	return loc
}
