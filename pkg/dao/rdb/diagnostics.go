package rdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// DiagnosticsDAO reads how the database is doing, for an administrator who looks for why a page is slow (P11 B0, B6). It reads only the
// statistics the database keeps about itself and the pool of this process, never the rows of the tables, and each read has a time limit.
type DiagnosticsDAO struct {
	db *gorm.DB
}

func NewDiagnosticsDAO(db *gorm.DB) *DiagnosticsDAO { return &DiagnosticsDAO{db: db} }

// PoolStats is the connection pool of this process: what is open, what is in use, and how long statements waited for a connection. A wait count
// that grows means the pool is too small for the load, which is a 504 that no query plan explains.
type PoolStats struct {
	MaxOpen         int   `json:"max_open"`
	Open            int   `json:"open"`
	InUse           int   `json:"in_use"`
	Idle            int   `json:"idle"`
	WaitCount       int64 `json:"wait_count"`
	WaitMillis      int64 `json:"wait_ms"`
	MaxIdleClosed   int64 `json:"max_idle_closed"`
	MaxLifeClosed   int64 `json:"max_lifetime_closed"`
	MaxIdleTimeKill int64 `json:"max_idle_time_closed"`
}

// TableStat is one table: how big it is and how many rows it holds. Rows are the database's own estimate (a count of a table of millions of
// rows is itself a slow query), or for SQLite a count that stops at MaxCountedRows.
type TableStat struct {
	Name       string     `json:"name"`
	Rows       int64      `json:"rows"`
	RowsCapped bool       `json:"rows_capped"`
	SizeBytes  int64      `json:"size_bytes"`
	IndexBytes int64      `json:"index_bytes"`
	DeadRows   int64      `json:"dead_rows"`
	LastVacuum *time.Time `json:"last_vacuum"`
}

// IndexStat is one index of a table, with how often it was used since the statistics were reset (PostgreSQL).
type IndexStat struct {
	Table     string `json:"table"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Scans     int64  `json:"scans"`
}

// RunningQuery is a statement the database is running now (PostgreSQL), the longest first.
type RunningQuery struct {
	RunningMillis int64  `json:"running_ms"`
	State         string `json:"state"`
	WaitType      string `json:"wait_type"`
	Query         string `json:"query"`
}

// WriteRate is how many rows a module wrote to the forecast table in the last hour (PostgreSQL and MySQL).
type WriteRate struct {
	ModuleID uint  `json:"module_id"`
	Rows     int64 `json:"rows"`
}

// DatabaseReport is what the diagnostics say. Sections the driver cannot answer are empty and named in Unavailable.
type DatabaseReport struct {
	Driver      string         `json:"driver"`
	Pool        PoolStats      `json:"pool"`
	Tables      []TableStat    `json:"tables"`
	Indexes     []IndexStat    `json:"indexes"`
	Running     []RunningQuery `json:"running"`
	Writes      []WriteRate    `json:"writes"`
	Unavailable []string       `json:"unavailable"`
}

// diagnosedTables are the tables that grow with the number of modules and the time the platform has run: the ones a slow page is waiting on.
var diagnosedTables = []string{"forecast_prediction", "metric_sample", "prediction_evaluation", "scaling_execution", "recommendation", "backtest_run",
	"algorithm_score", "admission_decision", "forecast_training_run", "scale_outcome", "job", "operation_log"}

// diagnosticsTimeout bounds each read: the diagnostics must not become the slow query.
const diagnosticsTimeout = 8 * time.Second

func (dao *DiagnosticsDAO) pool(sqlDB *sql.DB) PoolStats {
	s := sqlDB.Stats()
	return PoolStats{MaxOpen: s.MaxOpenConnections, Open: s.OpenConnections, InUse: s.InUse, Idle: s.Idle, WaitCount: s.WaitCount, WaitMillis: s.WaitDuration.Milliseconds(),
		MaxIdleClosed: s.MaxIdleClosed, MaxLifeClosed: s.MaxLifetimeClosed, MaxIdleTimeKill: s.MaxIdleTimeClosed}
}

// Report reads the diagnostics of the database this process uses.
func (dao *DiagnosticsDAO) Report(ctx context.Context) (*DatabaseReport, error) {
	sqlDB, err := dao.db.DB()
	if err != nil {
		return nil, err
	}
	out := &DatabaseReport{Driver: dao.db.Dialector.Name(), Pool: dao.pool(sqlDB), Tables: []TableStat{}, Indexes: []IndexStat{}, Running: []RunningQuery{}, Writes: []WriteRate{},
		Unavailable: []string{}}
	read := func(section string, fn func(ctx context.Context) error) {
		c, cancel := context.WithTimeout(ctx, diagnosticsTimeout)
		defer cancel()
		if err := fn(c); err != nil {
			out.Unavailable = append(out.Unavailable, fmt.Sprintf("%s: %v", section, err))
		}
	}
	switch out.Driver {
	case "postgres":
		read("tables", dao.postgresTables(out))
		read("indexes", dao.postgresIndexes(out))
		read("running", dao.postgresRunning(out))
		read("writes", dao.writes(out))
	case "mysql":
		read("tables", dao.mysqlTables(out))
		read("writes", dao.writes(out))
		out.Unavailable = append(out.Unavailable, "indexes and running statements: read only on PostgreSQL")
	default:
		read("tables", dao.countedTables(out))
		out.Unavailable = append(out.Unavailable, "sizes, indexes, running statements and write rates: read only on PostgreSQL and MySQL")
	}
	return out, nil
}

func (dao *DiagnosticsDAO) postgresTables(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		type row struct {
			Name       string
			Rows       int64
			SizeBytes  int64
			IndexBytes int64
			DeadRows   int64
			LastVacuum *time.Time
		}
		var rows []row
		err := dao.db.WithContext(ctx).Raw(`SELECT c.relname AS name, c.reltuples::bigint AS rows, pg_table_size(c.oid) AS size_bytes, pg_indexes_size(c.oid) AS index_bytes,
			COALESCE(s.n_dead_tup, 0) AS dead_rows, COALESCE(s.last_autovacuum, s.last_vacuum) AS last_vacuum
			FROM pg_class c LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
			WHERE c.relkind = 'r' AND c.relname IN ? ORDER BY pg_total_relation_size(c.oid) DESC`, diagnosedTables).Scan(&rows).Error
		for _, r := range rows {
			out.Tables = append(out.Tables, TableStat{Name: r.Name, Rows: max(r.Rows, 0), SizeBytes: r.SizeBytes, IndexBytes: r.IndexBytes, DeadRows: r.DeadRows, LastVacuum: r.LastVacuum})
		}
		return err
	}
}

func (dao *DiagnosticsDAO) postgresIndexes(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		type row struct {
			Table     string
			Name      string
			SizeBytes int64
			Scans     int64
		}
		var rows []row
		err := dao.db.WithContext(ctx).Raw(`SELECT relname AS "table", indexrelname AS name, pg_relation_size(indexrelid) AS size_bytes, idx_scan AS scans
			FROM pg_stat_user_indexes WHERE relname IN ? ORDER BY pg_relation_size(indexrelid) DESC LIMIT 40`, diagnosedTables).Scan(&rows).Error
		for _, r := range rows {
			out.Indexes = append(out.Indexes, IndexStat{Table: r.Table, Name: r.Name, SizeBytes: r.SizeBytes, Scans: r.Scans})
		}
		return err
	}
}

func (dao *DiagnosticsDAO) postgresRunning(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		type row struct {
			RunningMillis int64
			State         string
			WaitType      string
			Query         string
		}
		var rows []row
		err := dao.db.WithContext(ctx).Raw(`SELECT (EXTRACT(EPOCH FROM now() - query_start) * 1000)::bigint AS running_millis, COALESCE(state, '') AS state,
			COALESCE(wait_event_type, '') AS wait_type, left(query, 160) AS query
			FROM pg_stat_activity WHERE state <> 'idle' AND pid <> pg_backend_pid() AND datname = current_database() ORDER BY query_start LIMIT 10`).Scan(&rows).Error
		for _, r := range rows {
			out.Running = append(out.Running, RunningQuery{RunningMillis: r.RunningMillis, State: r.State, WaitType: r.WaitType, Query: r.Query})
		}
		return err
	}
}

func (dao *DiagnosticsDAO) mysqlTables(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		type row struct {
			Name       string
			Rows       int64
			SizeBytes  int64
			IndexBytes int64
		}
		var rows []row
		err := dao.db.WithContext(ctx).Raw(`SELECT table_name AS name, COALESCE(table_rows, 0) AS `+"`rows`"+`, COALESCE(data_length, 0) AS size_bytes, COALESCE(index_length, 0) AS index_bytes
			FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ? ORDER BY data_length + index_length DESC`, diagnosedTables).Scan(&rows).Error
		for _, r := range rows {
			out.Tables = append(out.Tables, TableStat{Name: r.Name, Rows: r.Rows, SizeBytes: r.SizeBytes, IndexBytes: r.IndexBytes})
		}
		return err
	}
}

// countedTables is for SQLite, which keeps no estimate: the rows are counted, up to the bound.
func (dao *DiagnosticsDAO) countedTables(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		for _, name := range diagnosedTables {
			if !dao.db.Migrator().HasTable(name) {
				continue
			}
			n, err := boundedCount(dao.db.WithContext(ctx).Table(name))
			if err != nil {
				return err
			}
			out.Tables = append(out.Tables, TableStat{Name: name, Rows: n, RowsCapped: n >= int64(MaxCountedRows)})
		}
		return nil
	}
}

// writes counts the forecast rows written in the last hour for each module: which module floods the table.
func (dao *DiagnosticsDAO) writes(out *DatabaseReport) func(context.Context) error {
	return func(ctx context.Context) error {
		if !dao.db.Migrator().HasTable("forecast_prediction") {
			return nil
		}
		var rows []WriteRate
		err := dao.db.WithContext(ctx).Table("forecast_prediction").Select("module_id, count(*) AS rows").
			Where("created_at > ?", time.Now().UTC().Add(-time.Hour)).Group("module_id").Order("rows DESC").Limit(10).Scan(&rows).Error
		out.Writes = append(out.Writes, rows...)
		return err
	}
}
