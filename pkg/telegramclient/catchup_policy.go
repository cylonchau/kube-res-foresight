package telegramclient

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// SysConfig keys of the catch-up spacing. They are edited on the system settings page.
const (
	ConfigCatchUpMinBacklog = "telegram.catchup.min_backlog"
	ConfigCatchUpDelay      = "telegram.catchup.delay_seconds"
	ConfigCatchUpJitter     = "telegram.catchup.jitter_seconds"
)

// CatchUpPolicy spaces out the messages taken in after a stop. A short gap is forwarded at once; once
// MinBacklog or more messages are waiting they go out one by one, each after Delay plus a random 0..Jitter
// seconds, so a restart never fires the whole backlog at the targets in one burst.
type CatchUpPolicy struct {
	MinBacklog int
	Delay      time.Duration
	Jitter     time.Duration
}

// DefaultCatchUpPolicy is used when nothing is configured.
var DefaultCatchUpPolicy = CatchUpPolicy{MinBacklog: 2, Delay: 30 * time.Second, Jitter: 30 * time.Second}

// maxCatchUpSpan keeps a long backlog inside the cache lifetime: if the spacing would stretch the last message
// beyond it, every gap is scaled down to fit, so nothing expires unsent.
const maxCatchUpSpan = 20 * time.Hour

// settingsSource reads one setting; *rdb.SysConfigDAO implements it.
type settingsSource interface {
	GetValueOrDefault(ctx context.Context, key, fallback string) (string, error)
}

// LoadCatchUpPolicy reads the policy; a missing or unreadable value falls back to the default.
func LoadCatchUpPolicy(ctx context.Context, src settingsSource) CatchUpPolicy {
	policy := DefaultCatchUpPolicy
	if src == nil {
		return policy
	}
	number := func(key string, fallback int) int {
		raw, err := src.GetValueOrDefault(ctx, key, "")
		raw = strings.TrimSpace(raw)
		if err != nil || raw == "" || raw == "-" {
			return fallback
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return fallback
		}
		return n
	}
	policy.MinBacklog = number(ConfigCatchUpMinBacklog, policy.MinBacklog)
	policy.Delay = time.Duration(number(ConfigCatchUpDelay, int(policy.Delay/time.Second))) * time.Second
	policy.Jitter = time.Duration(number(ConfigCatchUpJitter, int(policy.Jitter/time.Second))) * time.Second
	return policy
}

// Offsets returns, for a backlog of n messages, how long after "now" each one may be forwarded. A backlog
// below MinBacklog gets no delay at all. random returns a value in [0, n).
func (p CatchUpPolicy) Offsets(n int, random func(n int64) int64) []time.Duration {
	out := make([]time.Duration, n)
	if n < 2 || n < p.MinBacklog || (p.Delay <= 0 && p.Jitter <= 0) {
		return out
	}
	var total time.Duration
	for i := range out {
		step := p.Delay
		if p.Jitter > 0 {
			step += time.Duration(random(int64(p.Jitter/time.Second)+1)) * time.Second
		}
		if i > 0 { // the first message goes out at once; each later one waits after the previous
			total += step
		}
		out[i] = total
	}
	if total > maxCatchUpSpan {
		scale := float64(maxCatchUpSpan) / float64(total)
		for i := range out {
			out[i] = time.Duration(float64(out[i]) * scale)
		}
	}
	return out
}

func defaultRandom(n int64) int64 { return rand.Int63n(n) }
