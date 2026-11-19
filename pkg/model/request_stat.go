package model

import "time"

func init() {
	RegisterModel(&RequestStat{})
	RegisterModel(&ProcessBoot{})
}

// RequestStat is what one process saw of one route during one hour (P11 B0): the count, the 5xx, the stops at the platform's own time limit, the
// requests the other side gave up on, a histogram of the time (the buckets are reqstat.Bounds, comma separated) and how much of the time was in the
// database and in Prometheus. A process writes the absolute figures of its own hour again whenever they change, so a row is replaced, never added to,
// and two processes (or a process before and after a restart) never overwrite each other: the key has the boot id. It holds no request content.
type RequestStat struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	UpdatedAt time.Time `json:"-"`
	Hour      time.Time `gorm:"not null;uniqueIndex:udx_request_stat,priority:2;index:idx_request_stat_hour" json:"hour"`
	BootID    string    `gorm:"type:varchar(40);not null;uniqueIndex:udx_request_stat,priority:1" json:"boot_id"`
	Method    string    `gorm:"type:varchar(8);not null;uniqueIndex:udx_request_stat,priority:3" json:"method"`
	Route     string    `gorm:"type:varchar(200);not null;uniqueIndex:udx_request_stat,priority:4" json:"route"`

	Count     int64  `gorm:"not null;default:0" json:"count"`
	Status4xx int64  `gorm:"column:status_4xx;not null;default:0" json:"status_4xx"`
	Status5xx int64  `gorm:"column:status_5xx;not null;default:0" json:"status_5xx"`
	Timeouts  int64  `gorm:"not null;default:0" json:"timeouts"`
	Abandoned int64  `gorm:"not null;default:0" json:"abandoned"`
	SumMs     int64  `gorm:"not null;default:0" json:"sum_ms"`
	MaxMs     int64  `gorm:"not null;default:0" json:"max_ms"`
	DBMs      int64  `gorm:"column:db_ms;not null;default:0" json:"db_ms"`
	DBCalls   int64  `gorm:"column:db_calls;not null;default:0" json:"db_calls"`
	PromMs    int64  `gorm:"column:prom_ms;not null;default:0" json:"prom_ms"`
	PromCalls int64  `gorm:"column:prom_calls;not null;default:0" json:"prom_calls"`
	Buckets   string `gorm:"type:varchar(400);not null;default:''" json:"-"`
	Examples  string `gorm:"type:text" json:"-"`
}

func (RequestStat) TableName() string { return "request_stat" }

// ProcessBoot is one run of the platform process (P11 B0). A 502 that comes with a restart is a different problem from one that does not, and the
// platform cannot tell from inside whether it was killed, so it writes when it started, when it was last seen alive (with its memory and the limit it
// runs under), and whether it was stopped on purpose: a process that received SIGTERM (a rolling update, a failed probe, a deletion) writes Graceful;
// one that vanishes without it was killed (an out-of-memory kill is the usual cause), crashed, or lost with its node.
type ProcessBoot struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	BootID    string     `gorm:"type:varchar(40);not null;uniqueIndex" json:"boot_id"`
	Instance  string     `gorm:"type:varchar(128);not null;index" json:"instance"`
	Version   string     `gorm:"type:varchar(64)" json:"version"`
	StartedAt time.Time  `gorm:"not null;index" json:"started_at"`
	LastSeen  time.Time  `gorm:"not null" json:"last_seen"`
	StoppedAt *time.Time `json:"stopped_at"`
	// Graceful: the process was told to stop and finished. Unexpected: it was found gone with no stop written.
	Graceful   bool  `gorm:"not null;default:false" json:"graceful"`
	Unexpected bool  `gorm:"not null;default:false" json:"unexpected"`
	HeapBytes  int64 `gorm:"not null;default:0" json:"heap_bytes"`
	// SysBytes is the memory the Go runtime has from the system; MemLimit the memory limit of the container (cgroup) or 0 when there is none.
	SysBytes int64 `gorm:"not null;default:0" json:"sys_bytes"`
	MemLimit int64 `gorm:"not null;default:0" json:"mem_limit"`
}

func (ProcessBoot) TableName() string { return "process_boot" }
