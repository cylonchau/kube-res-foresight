// Package resultcache keeps the answer of a heavy report for the next request that asks the same (P11 B3), and makes the requests that come while one is being worked
// out wait for it instead of working the same thing out again. An answer is kept with the version of the data it was made from, and it is used only while the
// version is the same and it is not older than its time: when the data changed, the cache has nothing, so a cached answer is never an old one, only one
// that nothing has changed under. The cache is in the memory of the process; a restart empties it, and each replica has its own.
package resultcache

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Source says where an answer came from.
type Source string

const (
	// Computed: the answer was worked out for this request.
	Computed Source = "miss"
	// Cached: an answer that was kept, made from the same version of the data.
	Cached Source = "hit"
	// Shared: another request was working the same thing out, and this one took its answer.
	Shared Source = "shared"
	// Bypassed: the cache is off, or the answer cannot be kept.
	Bypassed Source = "off"
)

// Result is how an answer was got.
type Result struct {
	Source Source
	// Age is how old a cached answer is.
	Age time.Duration
}

// Stats are the counters of a cache, for the diagnostics.
type Stats struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Shared    int64 `json:"shared"`
	Changed   int64 `json:"changed"`
	Expired   int64 `json:"expired"`
	Evictions int64 `json:"evictions"`
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	// HitRate is the share of the requests that did not work the answer out (a hit or an answer that was shared), 0 to 1.
	HitRate float64 `json:"hit_rate"`
}

type entry struct {
	key     string
	version string
	value   any
	size    int64
	at      time.Time
	ttl     time.Duration
	elem    *list.Element
}

type flight struct {
	done    chan struct{}
	version string
	value   any
	err     error
}

// Cache is an LRU of answers bounded by their size.
type Cache struct {
	mu       sync.Mutex
	items    map[string]*entry
	order    *list.List
	flights  map[string]*flight
	bytes    int64
	maxBytes int64
	maxItems int
	now      func() time.Time

	hits, misses, shared, changed, expired, evictions atomic.Int64
}

// New makes a cache that holds at most maxBytes (the size of the answers as they marshal) and maxItems answers.
func New(maxBytes int64, maxItems int) *Cache {
	return &Cache{items: map[string]*entry{}, order: list.New(), flights: map[string]*flight{}, maxBytes: maxBytes, maxItems: maxItems, now: time.Now}
}

var (
	defaultOnce sync.Once
	process     *Cache
)

// Default is the cache of the process.
func Default() *Cache {
	defaultOnce.Do(func() { process = New(64<<20, 1024) })
	return process
}

// SetClock replaces the clock; for tests.
func (c *Cache) SetClock(now func() time.Time) { c.now = now }

// Stats reads the counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Shared: c.shared.Load(), Changed: c.changed.Load(), Expired: c.expired.Load(), Evictions: c.evictions.Load(),
		Entries: len(c.items), Bytes: c.bytes, MaxBytes: c.maxBytes}
	if total := s.Hits + s.Misses + s.Shared; total > 0 {
		s.HitRate = float64(s.Hits+s.Shared) / float64(total)
	}
	return s
}

// Forget drops what is kept under the prefix (a module that was deleted, a setting that was changed).
func (c *Cache) Forget(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, e := range c.items {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.removeLocked(e)
		}
	}
}

func (c *Cache) removeLocked(e *entry) {
	c.order.Remove(e.elem)
	delete(c.items, e.key)
	c.bytes -= e.size
}

func (c *Cache) storeLocked(key, version string, value any, size int64, ttl time.Duration) {
	if old, ok := c.items[key]; ok {
		c.removeLocked(old)
	}
	if size > c.maxBytes {
		return // an answer bigger than the whole cache is not kept
	}
	e := &entry{key: key, version: version, value: value, size: size, at: c.now(), ttl: ttl}
	e.elem = c.order.PushFront(e)
	c.items[key] = e
	c.bytes += size
	for (c.bytes > c.maxBytes || len(c.items) > c.maxItems) && c.order.Len() > 1 {
		oldest := c.order.Back().Value.(*entry)
		c.removeLocked(oldest)
		c.evictions.Add(1)
	}
}

// Do returns the answer for the key at the version of the data: the one that is kept when it was made from this version and is not older than ttl, else the
// one another request is working out for this version, else the one it works out itself. ttl of 0 or less switches the cache off for the call (the answer is
// computed, nothing is kept). The answer is shared between the requests that get it: it must not be changed.
func Do[T any](ctx context.Context, c *Cache, key, version string, ttl time.Duration, compute func(context.Context) (T, error)) (T, Result, error) {
	var zero T
	if c == nil || ttl <= 0 {
		v, err := compute(ctx)
		return v, Result{Source: Bypassed}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		if e, ok := c.items[key]; ok {
			age := c.now().Sub(e.at)
			switch {
			case e.version != version:
				c.changed.Add(1)
				c.removeLocked(e)
			case age >= e.ttl || age >= ttl:
				c.expired.Add(1)
				c.removeLocked(e)
			default:
				c.order.MoveToFront(e.elem)
				v, ok := e.value.(T)
				c.mu.Unlock()
				if ok {
					c.hits.Add(1)
					return v, Result{Source: Cached, Age: age}, nil
				}
				// a key used for two kinds of answer: not an answer for this caller
				c.Forget(key)
				continue
			}
		}
		if f, ok := c.flights[key]; ok && f.version == version {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return zero, Result{Source: Shared}, ctx.Err()
			}
			if f.err != nil {
				// the request that was working it out was stopped (its client went away or its time ran out): this one asks for itself, once
				if attempt == 0 && ctx.Err() == nil {
					continue
				}
				return zero, Result{Source: Shared}, f.err
			}
			if v, ok := f.value.(T); ok {
				c.shared.Add(1)
				return v, Result{Source: Shared}, nil
			}
			continue
		}
		f := &flight{done: make(chan struct{}), version: version}
		c.flights[key] = f
		c.mu.Unlock()

		c.misses.Add(1)
		value, err := compute(ctx)
		f.value, f.err = value, err
		c.mu.Lock()
		if c.flights[key] == f {
			delete(c.flights, key)
		}
		if err == nil {
			if raw, merr := json.Marshal(value); merr == nil {
				c.storeLocked(key, version, value, int64(len(raw)), ttl)
			}
		}
		c.mu.Unlock()
		close(f.done)
		return value, Result{Source: Computed}, err
	}
	v, err := compute(ctx)
	return v, Result{Source: Bypassed}, err
}

// Peek is the kept answer for the key when it was made from this version and is still good, without working anything out; it counts as a hit when there is one.
func Peek[T any](c *Cache, key, version string, ttl time.Duration) (T, bool) {
	var zero T
	if c == nil || ttl <= 0 {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || e.version != version {
		return zero, false
	}
	if age := c.now().Sub(e.at); age >= e.ttl || age >= ttl {
		return zero, false
	}
	v, ok := e.value.(T)
	if !ok {
		return zero, false
	}
	c.order.MoveToFront(e.elem)
	c.hits.Add(1)
	return v, true
}
