package controller

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The outbox keeps what the operator did, so that a platform that cannot be reached does not make the record of it disappear. Every
// report is queued first and sent after: a send that fails leaves the report in the queue, and the queue is sent again (oldest first, so
// the platform sees the actions in the order they were taken) when the platform answers. The platform stores a report once: each carries a
// record id the operator made (see Report.RecordID), and a report that arrives twice (the answer was lost, the operator restarted before
// it knew) is acknowledged the second time without a second row.
//
// The queue is held in memory and, when File is set, written to a spool file at each change, so a restart of the operator in the middle of
// an outage does not lose it either (the file belongs in a volume that outlives the container, an emptyDir for instance). It is bounded: at
// Max entries the oldest is dropped, counted and logged, because a platform that stays away for days must not grow the operator without
// limit. A report the platform refuses for good (it does not know the module any more, the report is malformed) is dropped the same way
// instead of blocking the ones behind it. Nothing else is ever dropped.

// DefaultOutboxMax is how many reports the outbox holds: at the pace of one report per module per sync round, a day of outage of a
// hundred modules.
const DefaultOutboxMax = 10000

// DefaultOutboxFlushInterval is how often the queue is tried while the platform is away.
const DefaultOutboxFlushInterval = 15 * time.Second

// Outbox is a Reporter that delivers through Sink with the guarantees above. It is also a manager Runnable: Start sends the queue in the
// background so that an evaluation never waits for the platform.
type Outbox struct {
	// Sink delivers one report; an error that is not a permanent refusal leaves the report queued.
	Sink Reporter
	// Max bounds the queue; 0 takes DefaultOutboxMax.
	Max int
	// File is the spool file; empty keeps the queue in memory only.
	File string
	// Interval is how often a queue that could not be sent is tried again; 0 takes DefaultOutboxFlushInterval.
	Interval time.Duration
	// Operator names this operator (the Pod) in each record; empty takes the host name.
	Operator string
	Metrics  *Metrics
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time

	instance string
	seq      atomic.Uint64
	kick     chan struct{}
	once     sync.Once

	flushMu sync.Mutex
	mu      sync.Mutex
	queue   []queued
}

type queued struct {
	Module string `json:"module"`
	Report Report `json:"report"`
}

func (o *Outbox) init() {
	o.once.Do(func() {
		o.kick = make(chan struct{}, 1)
		var b [4]byte
		_, _ = rand.Read(b[:])
		o.instance = hex.EncodeToString(b[:])
		if o.Operator == "" {
			o.Operator, _ = os.Hostname()
		}
	})
}

func (o *Outbox) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Outbox) max() int {
	if o.Max > 0 {
		return o.Max
	}
	return DefaultOutboxMax
}

// NeedLeaderElection is false: a replica that lost the lease still sends what it has queued.
func (o *Outbox) NeedLeaderElection() bool { return false }

// Pending is how many reports wait to be sent.
func (o *Outbox) Pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.queue)
}

// Load reads the spool file into the queue; a missing file is an empty queue. It is called once before the outbox starts.
func (o *Outbox) Load() error {
	o.init()
	if o.File == "" {
		return nil
	}
	f, err := os.Open(o.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the report spool: %w", err)
	}
	defer f.Close()
	var loaded []queued
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var q queued
		if err := json.Unmarshal(sc.Bytes(), &q); err != nil || q.Report.RecordID == "" {
			continue // a torn last line from a crash: the rest is still good
		}
		loaded = append(loaded, q)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read the report spool: %w", err)
	}
	o.mu.Lock()
	o.queue = append(loaded, o.queue...)
	o.mu.Unlock()
	o.Metrics.recordOutbox(len(loaded))
	return nil
}

// persist writes the queue to the spool file (whole, through a temporary file, so a crash leaves the old or the new one, not half).
// The caller holds mu.
func (o *Outbox) persist() error {
	if o.File == "" {
		return nil
	}
	if dir := filepath.Dir(o.File); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := o.File + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, q := range o.queue {
		if err := enc.Encode(q); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, o.File)
}

// Report implements Reporter: it makes the record id, queues the report and wakes the sender. It does not wait for the platform.
func (o *Outbox) Report(ctx context.Context, s *Scaler, r Report) error {
	o.init()
	r.Module = s.Module
	if r.OccurredAt.IsZero() {
		r.OccurredAt = o.now()
	}
	if r.RecordID == "" {
		r.RecordID = fmt.Sprintf("%s-%d", o.instance, o.seq.Add(1))
	}
	if r.Operator == "" {
		r.Operator = o.Operator
	}
	o.mu.Lock()
	o.queue = append(o.queue, queued{Module: s.Module, Report: r})
	if over := len(o.queue) - o.max(); over > 0 {
		o.queue = append([]queued(nil), o.queue[over:]...)
		o.Metrics.recordOutboxDrop("overflow", over)
		logf.FromContext(ctx).WithName("outbox").Info("the report queue is full: the oldest reports were dropped", "dropped", over, "max", o.max())
	}
	err := o.persist()
	n := len(o.queue)
	o.mu.Unlock()
	o.Metrics.recordOutbox(n)
	select {
	case o.kick <- struct{}{}:
	default:
	}
	if err != nil {
		// the report is queued in memory and will be sent; only the spool could not be written
		logf.FromContext(ctx).WithName("outbox").Info("could not write the report spool", "error", err.Error())
	}
	return nil
}

// permanent says the platform refused the report for good: sending it again cannot succeed. A credential or a gateway problem is not
// permanent, it is the very outage the outbox is for.
func permanent(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// Flush sends the queue, oldest first, until it is empty or the platform cannot be reached. It is safe to call from several places.
func (o *Outbox) Flush(ctx context.Context) {
	o.init()
	o.flushMu.Lock()
	defer o.flushMu.Unlock()
	log := logf.FromContext(ctx).WithName("outbox")
	for ctx.Err() == nil {
		o.mu.Lock()
		if len(o.queue) == 0 {
			o.mu.Unlock()
			return
		}
		head := o.queue[0]
		o.mu.Unlock()

		err := o.Sink.Report(ctx, &Scaler{Module: head.Module}, head.Report)
		switch {
		case err == nil:
		case permanent(err):
			log.Info("the platform refused a report for good: dropped", "record", head.Report.RecordID, "module", head.Module, "error", err.Error())
			o.Metrics.recordOutboxDrop("rejected", 1)
		default:
			return // the platform cannot be reached: the queue stays as it is
		}
		o.mu.Lock()
		if len(o.queue) > 0 && o.queue[0].Report.RecordID == head.Report.RecordID {
			o.queue = o.queue[1:]
		}
		if perr := o.persist(); perr != nil {
			log.Info("could not write the report spool", "error", perr.Error())
		}
		n := len(o.queue)
		o.mu.Unlock()
		o.Metrics.recordOutbox(n)
	}
}

// Start sends the queue whenever a report is added and every Interval, until ctx ends; it tries once more on the way out.
func (o *Outbox) Start(ctx context.Context) error {
	o.init()
	interval := o.Interval
	if interval <= 0 {
		interval = DefaultOutboxFlushInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	o.Flush(ctx) // what an earlier run left in the spool
	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			o.Flush(last)
			cancel()
			return nil
		case <-o.kick:
			o.Flush(ctx)
		case <-tick.C:
			o.Flush(ctx)
		}
	}
}
