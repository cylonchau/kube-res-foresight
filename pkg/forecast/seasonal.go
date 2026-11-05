package forecast

import (
	"fmt"
	"math"
	"time"
)

// Seasonal is implemented by an algorithm whose forecast is a repeating shape (a day, a week) learned from the history. SeasonSpan is the
// longest repeat it learns, in time (step is the step of the series, for the ones that count it in steps).
//
// It lets the platform ask how many independent repeats a history holds before the algorithm is trusted: a daily shape learned from one
// day is that one day, odd as it may have been, and a peak it "foresees" may be nothing but that day's incident.
type Seasonal interface {
	SeasonSpan(step time.Duration) time.Duration
}

// SeasonSpanOf is the longest repeat the model learns, or 0 when it learns none (it is not seasonal) or when it cannot tell.
func SeasonSpanOf(m Model, step time.Duration) time.Duration {
	if s, ok := m.(Seasonal); ok {
		return s.SeasonSpan(step)
	}
	return 0
}

func (m SeasonalNaive) SeasonSpan(step time.Duration) time.Duration {
	return time.Duration(m.Season) * step
}

func (m HoltWinters) SeasonSpan(step time.Duration) time.Duration {
	if !m.seasonal() {
		return 0
	}
	return time.Duration(m.Season) * step
}

func (m Theta) SeasonSpan(step time.Duration) time.Duration {
	if !m.seasonal() {
		return 0
	}
	return time.Duration(m.Season) * step
}

func (m MSTL) SeasonSpan(step time.Duration) time.Duration {
	longest := 0
	for _, p := range m.periods() {
		longest = max(longest, p)
	}
	return time.Duration(longest) * step
}

// SeasonSpan of the seasonal profile is a week when it tells the days of the week apart, else a day: its buckets are the time of the week
// (or of the day), whatever the step.
func (m SeasonalProfile) SeasonSpan(time.Duration) time.Duration {
	if m.ByWeekday {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// SeasonSpan of a model behind the short-term correction is that of the model it wraps.
func (m ShortTerm) SeasonSpan(step time.Duration) time.Duration { return SeasonSpanOf(m.Inner, step) }

// Conservative is implemented by an algorithm whose forecast is deliberately high (a percentile, an envelope of the peaks), so that it
// is not read as the load that is expected to come: it is a level that is enough most of the time. Conservatism says what it is, short
// ("P90", "peak envelope").
type Conservative interface {
	Conservatism() string
}

// ConservatismOf says what a deliberately high forecast is, or "" for one that is the expected load (a mean, a last value, a seasonal copy).
func ConservatismOf(m Model) string {
	if c, ok := m.(Conservative); ok {
		return c.Conservatism()
	}
	if q, ok := m.(interface{ QuantileLevel() (float64, bool) }); ok {
		if level, isQuantile := q.QuantileLevel(); isQuantile && level > 0.5 {
			return fmt.Sprintf("P%g", math.Round(level*1000)/10)
		}
	}
	return ""
}

func (m HistPercentile) Conservatism() string { return fmt.Sprintf("P%g", m.Percentile) }

func (m SeasonalProfile) Conservatism() string { return fmt.Sprintf("P%g", m.Percentile) }

func (PeakEnvelope) Conservatism() string { return "peak envelope" }

func (m ShortTerm) Conservatism() string { return ConservatismOf(m.Inner) }
