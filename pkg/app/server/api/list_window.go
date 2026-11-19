package api

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// searchWindow gives a text search that names no time range a range: the last http.list_search_days days. A text search in a table that grows
// reads every row it looks at, because no index serves a LIKE '%word%'; the time range is what an index can cut. It returns the since the
// query should use and, when it was the platform that set it, how many days that is (0 when the request had its own range or did not search),
// so the answer can say what it read.
func searchWindow(ctx context.Context, cfg *rdb.SysConfigDAO, searching bool, since *time.Time) (*time.Time, int) {
	if !searching || since != nil {
		return since, 0
	}
	days := timing.ListSearchDays.Default
	if cfg != nil {
		if raw, err := cfg.GetValueOrDefault(ctx, timing.ListSearchDays.Key, ""); err == nil {
			days = timing.ListSearchDays.Parse(raw)
		}
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	return &from, days
}

// summaryWindow gives a summary that names no start the last http.summary_window_hours hours, and says how many (0 when the request had its own).
func summaryWindow(ctx context.Context, cfg *rdb.SysConfigDAO, since *time.Time) (*time.Time, int) {
	if since != nil {
		return since, 0
	}
	hours := timing.SummaryWindowHours.Default
	if cfg != nil {
		if raw, err := cfg.GetValueOrDefault(ctx, timing.SummaryWindowHours.Key, ""); err == nil {
			hours = timing.SummaryWindowHours.Parse(raw)
		}
	}
	from := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	return &from, hours
}
