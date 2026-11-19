package reqstat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// RetentionDays is how long the hours and the runs are kept.
const RetentionDays = 14

// Service stores what the recorder of this process collects, keeps the record of its runs, and reads the baseline back.
type Service struct {
	Recorder *Recorder
	Stats    *rdb.RequestStatDAO
	Boots    *rdb.ProcessBootDAO
	BootID   string
	Instance string
	Version  string
	Now      func() time.Time
	// FlushEvery and BeatEvery are the intervals of storing the hours and of saying the process is alive.
	FlushEvery time.Duration
	BeatEvery  time.Duration
	// LostAfter is how long a run is not seen before it is taken for gone.
	LostAfter time.Duration
	// MemLimit reads the memory limit of the container; the cgroup file by default.
	MemLimit func() int64

	stopOnce sync.Once
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// New makes the service of this process with a new boot id.
func New(stats *rdb.RequestStatDAO, boots *rdb.ProcessBootDAO, rec *Recorder, version string) *Service {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	host, _ := os.Hostname()
	return &Service{Recorder: rec, Stats: stats, Boots: boots, BootID: time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(raw), Instance: host, Version: version}
}

func (s *Service) flushEvery() time.Duration {
	if s.FlushEvery > 0 {
		return s.FlushEvery
	}
	return time.Minute
}

func (s *Service) beatEvery() time.Duration {
	if s.BeatEvery > 0 {
		return s.BeatEvery
	}
	return 30 * time.Second
}

func (s *Service) lostAfter() time.Duration {
	if s.LostAfter > 0 {
		return s.LostAfter
	}
	return 3 * time.Minute
}

func (s *Service) memLimit() int64 {
	if s.MemLimit != nil {
		return s.MemLimit()
	}
	return ContainerMemoryLimit()
}

// ContainerMemoryLimit is the memory limit of the container as the cgroup says it (v2, then v1), or 0 when there is none or it cannot be read.
func ContainerMemoryLimit() int64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if text == "" || text == "max" {
			return 0
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n <= 0 || n >= 1<<60 {
			return 0
		}
		return n
	}
	if limit := debug.SetMemoryLimit(-1); limit > 0 && limit < 1<<60 {
		return limit
	}
	return 0
}

// Start writes this run, and then, until ctx ends, stores the changed hours every minute, says the process is alive every half minute, marks the runs
// that vanished and drops what is older than the retention. It returns at once; the work is in a goroutine.
func (s *Service) Start(ctx context.Context) {
	if s.Boots == nil || s.Stats == nil {
		return
	}
	now := s.now()
	boot := &model.ProcessBoot{BootID: s.BootID, Instance: s.Instance, Version: s.Version, StartedAt: now, LastSeen: now}
	if err := s.Boots.Start(ctx, boot); err != nil {
		logger.Warn("The start of the process could not be recorded", logger.Err(err))
	}
	go func() {
		flush := time.NewTicker(s.flushEvery())
		beat := time.NewTicker(s.beatEvery())
		prune := time.NewTicker(time.Hour)
		defer flush.Stop()
		defer beat.Stop()
		defer prune.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-flush.C:
				s.Flush(ctx)
			case <-beat.C:
				s.Beat(ctx)
			case <-prune.C:
				s.Prune(ctx)
			}
		}
	}()
}

// Flush stores the hours that changed.
func (s *Service) Flush(ctx context.Context) {
	hours := s.Recorder.TakeChanged()
	if len(hours) == 0 {
		return
	}
	rows := make([]model.RequestStat, 0, len(hours))
	for _, h := range hours {
		rows = append(rows, toRow(s.BootID, h))
	}
	if err := s.Stats.Upsert(ctx, rows); err != nil {
		s.Recorder.Redirty(hours)
		logger.Warn("The request statistics could not be stored", logger.Err(err))
	}
}

// Beat says the process is alive, with its memory, and marks the runs of other Pods that are gone.
func (s *Service) Beat(ctx context.Context) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	now := s.now()
	if err := s.Boots.Beat(ctx, s.BootID, now, int64(m.HeapAlloc), int64(m.Sys), s.memLimit()); err != nil {
		logger.Warn("The heartbeat of the process could not be stored", logger.Err(err))
	}
	if n, err := s.Boots.SweepLost(ctx, now, s.lostAfter()); err == nil && n > 0 {
		logger.Warn("A process was not seen any more and wrote no stop: it was killed, crashed or lost with its node", logger.Int64("runs", n))
	}
}

// Prune drops the statistics and the runs older than the retention.
func (s *Service) Prune(ctx context.Context) {
	cutoff := s.now().AddDate(0, 0, -RetentionDays)
	_, _ = s.Stats.PruneBefore(ctx, cutoff)
	_, _ = s.Boots.PruneBefore(ctx, cutoff)
}

// Stop is called when the process was told to stop: the last hours are stored and the run is written as stopped on purpose.
func (s *Service) Stop() {
	if s == nil || s.Boots == nil {
		return
	}
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Flush(ctx)
		if err := s.Boots.Stop(ctx, s.BootID, s.now()); err != nil {
			logger.Warn("The stop of the process could not be recorded", logger.Err(err))
		}
	})
}

// Report is the baseline of the routes over the last hours, the runs of the process, and what they say.
type Report struct {
	Hours    int                 `json:"hours"`
	From     time.Time           `json:"from"`
	To       time.Time           `json:"to"`
	Routes   []RouteSummary      `json:"routes"`
	Boots    []model.ProcessBoot `json:"boots"`
	Findings []Finding           `json:"findings"`
	// BucketBounds are the upper edges in milliseconds of the buckets of each route's histogram.
	BucketBounds []int64 `json:"bucket_bounds"`
	GatewayS     int     `json:"gateway_seconds"`
	ListS        int     `json:"list_seconds"`
	RetentionDay int     `json:"retention_days"`
}

// Read stores what this process has so far and reads the baseline of the last hours.
func (s *Service) Read(ctx context.Context, hours int, lim Limits) (*Report, error) {
	if hours < 1 {
		hours = 24
	}
	if hours > RetentionDays*24 {
		hours = RetentionDays * 24
	}
	s.Flush(ctx)
	now := s.now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	rows, err := s.Stats.Since(ctx, since)
	if err != nil {
		return nil, err
	}
	boots, err := s.Boots.Since(ctx, since)
	if err != nil {
		return nil, err
	}
	routes := Summarize(rows)
	return &Report{Hours: hours, From: since, To: now, Routes: routes, Boots: boots, Findings: Diagnose(routes, boots, lim), BucketBounds: Bounds,
		GatewayS: int(lim.Gateway / time.Second), ListS: int(lim.ListBudget / time.Second), RetentionDay: RetentionDays}, nil
}

var (
	defaultRecorder = NewRecorder()
	defaultMu       sync.RWMutex
	defaultService  *Service
)

// DefaultRecorder is the recorder of this process, the one the HTTP middleware writes to.
func DefaultRecorder() *Recorder { return defaultRecorder }

// SetDefault makes the service the one the diagnostics read.
func SetDefault(s *Service) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultService = s
}

// Default is the service of this process; nil before the server has set it up.
func Default() *Service {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultService
}
