package store

import (
	"database/sql"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/utils"
	"gorm.io/gorm"
	"net/http"
	_ "net/http/pprof" // registered on the default mux; served only when KUBE_RES_FORESIGHT_PPROF_ADDR is set
	"regexp"
	"sort"
	"sync"
	"time"
)

// statementStats counts the statements the process sends to the database, by operation and table, and how long each
// took as the caller saw it (waiting for the connection included). With SQLite there is one connection, so the table
// that tops this list is the one the pages and the signal runs queue behind (skill section 50). Once a minute the top of
// the list and the pool counters (how often and how long a caller waited for the connection) are written to the log.
type statementStats struct {
	mu      sync.Mutex
	entries map[string]*statementEntry
}

type statementEntry struct {
	count int
	total time.Duration
	max   time.Duration
}

var tablePattern = regexp.MustCompile(`(?i)(?:from|into|update)\s+[` + "`" + `"]?(\w+)`)

const statsStartKey = "kube_res_foresight:stmt_start"

func (s *statementStats) record(tx *gorm.DB, op string) {
	started, ok := tx.InstanceGet(statsStartKey)
	if !ok {
		return
	}
	elapsed := time.Since(started.(time.Time))
	table := "?"
	if tx.Statement != nil {
		if tx.Statement.Table != "" {
			table = tx.Statement.Table
		} else if m := tablePattern.FindStringSubmatch(tx.Statement.SQL.String()); m != nil {
			table = m[1]
		}
	}
	key := op + " " + table
	s.mu.Lock()
	e := s.entries[key]
	if e == nil {
		e = &statementEntry{}
		s.entries[key] = e
	}
	e.count++
	e.total += elapsed
	if elapsed > e.max {
		e.max = elapsed
	}
	s.mu.Unlock()
}

// takeTop empties the counters and gives the busiest entries by number of statements.
func (s *statementStats) takeTop(n int) (total int, top []map[string]any) {
	s.mu.Lock()
	entries := s.entries
	s.entries = map[string]*statementEntry{}
	s.mu.Unlock()
	keys := make([]string, 0, len(entries))
	for k, e := range entries {
		total += e.count
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return entries[keys[i]].count > entries[keys[j]].count })
	for i, k := range keys {
		if i >= n {
			break
		}
		e := entries[k]
		top = append(top, map[string]any{"what": k, "count": e.count, "avg_ms": (e.total / time.Duration(e.count)).Milliseconds(), "max_ms": e.max.Milliseconds()})
	}
	return total, top
}

// StartDiagnostics counts the statements of a database handle and logs the busiest ones, with the connection pool
// counters, once a minute. It also serves the Go profiler when KUBE_RES_FORESIGHT_PPROF_ADDR is set (for example 127.0.0.1:6060),
// which is how a process that uses a whole core can be asked what it is doing.
func StartDiagnostics(db *gorm.DB, sqlDB *sql.DB) {
	stats := &statementStats{entries: map[string]*statementEntry{}}
	start := func(tx *gorm.DB) { tx.InstanceSet(statsStartKey, time.Now()) }
	_ = db.Callback().Query().Before("gorm:query").Register("kube_res_foresight:stats_start_query", start)
	_ = db.Callback().Query().After("gorm:after_query").Register("kube_res_foresight:stats_query", func(tx *gorm.DB) { stats.record(tx, "SELECT") })
	_ = db.Callback().Row().Before("gorm:row").Register("kube_res_foresight:stats_start_row", start)
	_ = db.Callback().Row().After("gorm:row").Register("kube_res_foresight:stats_row", func(tx *gorm.DB) { stats.record(tx, "SELECT") })
	_ = db.Callback().Create().Before("gorm:begin_transaction").Register("kube_res_foresight:stats_start_create", start)
	_ = db.Callback().Create().After("gorm:commit_or_rollback_transaction").Register("kube_res_foresight:stats_create", func(tx *gorm.DB) { stats.record(tx, "INSERT") })
	_ = db.Callback().Update().Before("gorm:begin_transaction").Register("kube_res_foresight:stats_start_update", start)
	_ = db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("kube_res_foresight:stats_update", func(tx *gorm.DB) { stats.record(tx, "UPDATE") })
	_ = db.Callback().Delete().Before("gorm:begin_transaction").Register("kube_res_foresight:stats_start_delete", start)
	_ = db.Callback().Delete().After("gorm:commit_or_rollback_transaction").Register("kube_res_foresight:stats_delete", func(tx *gorm.DB) { stats.record(tx, "DELETE") })

	if addr := utils.ProjectEnv("KUBE_RES_FORESIGHT_PPROF_ADDR"); addr != "" {
		go func() {
			logger.Info("Serving the Go profiler", logger.String("addr", addr))
			_ = http.ListenAndServe(addr, nil)
		}()
	}
	go func() {
		var lastWaits int64
		var lastWaited time.Duration
		for range time.Tick(time.Minute) {
			total, top := stats.takeTop(8)
			pool := sqlDB.Stats()
			logger.Info("Database statements in the last minute",
				logger.Int("statements", total),
				logger.Any("busiest", top),
				logger.Int64("pool_waits", pool.WaitCount-lastWaits),
				logger.Int64("pool_waited_ms", (pool.WaitDuration-lastWaited).Milliseconds()),
				logger.Int("in_use", pool.InUse),
			)
			lastWaits, lastWaited = pool.WaitCount, pool.WaitDuration
		}
	}()
}
