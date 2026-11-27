package resultcache

import (
	"context"
	"errors"
	"sync"
)

// Progress is how far a heavy read that goes on by itself has come: the stage it is in and how many of its queries (or modules) are answered. It is what a request that
// had to stop waiting tells the page, so the page shows "reading the metrics (3/7)" instead of only a count of seconds.
type Progress struct {
	Stage string `json:"stage,omitempty"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type progress struct {
	mu sync.Mutex
	p  Progress
}

type progressKey struct{}

func progressOf(ctx context.Context) *progress {
	box, _ := ctx.Value(progressKey{}).(*progress)
	return box
}

// Expect says that n more units of work are coming under the stage (a stage names what the reads are about: the metrics of a module, the modules of the table). The
// units of several reads of one answer add up. Without a progress in the context (the cache is off, or the read is not worked out by Within) it does nothing.
func Expect(ctx context.Context, stage string, n int) {
	box := progressOf(ctx)
	if box == nil || n <= 0 {
		return
	}
	box.mu.Lock()
	box.p.Total += n
	if stage != "" {
		box.p.Stage = stage
	}
	box.mu.Unlock()
}

// Finished says that one unit of work is over, whatever its end was: a unit that failed or was stopped by the time limit is still not waited for.
func Finished(ctx context.Context) {
	box := progressOf(ctx)
	if box == nil {
		return
	}
	box.mu.Lock()
	if box.p.Done < box.p.Total {
		box.p.Done++
	}
	box.mu.Unlock()
}

func (b *progress) get() Progress {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.p
}

// PendingError is ErrPending with how far the work has come. errors.Is(err, ErrPending) holds for it.
type PendingError struct {
	Progress Progress
}

func (e *PendingError) Error() string { return ErrPending.Error() }

// Is makes a PendingError an ErrPending for errors.Is.
func (e *PendingError) Is(target error) bool { return target == ErrPending }

func pendingOf(f *flight) error {
	if f == nil || f.progress == nil {
		return ErrPending
	}
	return &PendingError{Progress: f.progress.get()}
}

// ProgressOf is the progress an error that ends a wait carries, when it carries one.
func ProgressOf(err error) (Progress, bool) {
	var pe *PendingError
	if errors.As(err, &pe) {
		return pe.Progress, true
	}
	return Progress{}, false
}
