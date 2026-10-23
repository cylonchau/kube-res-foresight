package job

import (
	"fmt"
	"github.com/robfig/cron/v3"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParseCron parses task schedules. Seven-field expressions use the form
// "millisecond second minute hour day-of-month month day-of-week". Six-field
// expressions start at seconds, and legacy five-field expressions start at
// minutes. @every accepts Go durations, including millisecond durations.
func ParseCron(expr string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "@every ") {
		d, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expr, "@every ")))
		if err != nil || d < time.Millisecond {
			return nil, fmt.Errorf("@every duration must be at least 1ms")
		}
		return durationSchedule{duration: d}, nil
	}

	fields := strings.Fields(expr)
	switch len(fields) {
	case 7:
		milliseconds, err := parseMilliseconds(fields[0])
		if err != nil {
			return nil, err
		}
		base, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).
			Parse(strings.Join(fields[1:], " "))
		if err != nil {
			return nil, err
		}
		return millisecondSchedule{base: base, milliseconds: milliseconds}, nil
	case 6:
		return cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(expr)
	case 5:
		return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(expr)
	default:
		return nil, fmt.Errorf("cron expression must have 5, 6, or 7 fields")
	}
}

type durationSchedule struct{ duration time.Duration }

func (s durationSchedule) Next(t time.Time) time.Time { return t.Add(s.duration) }

type millisecondSchedule struct {
	base         cron.Schedule
	milliseconds []int
}

func (s millisecondSchedule) Next(t time.Time) time.Time {
	second := t.Truncate(time.Second)
	if s.base.Next(second.Add(-time.Nanosecond)).Equal(second) {
		for _, ms := range s.milliseconds {
			candidate := second.Add(time.Duration(ms) * time.Millisecond)
			if candidate.After(t) {
				return candidate
			}
		}
	}
	nextSecond := s.base.Next(second)
	return nextSecond.Add(time.Duration(s.milliseconds[0]) * time.Millisecond)
}

func parseMilliseconds(field string) ([]int, error) {
	seen := make(map[int]struct{})
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty millisecond field")
		}
		step := 1
		base := part
		if strings.Contains(part, "/") {
			pieces := strings.Split(part, "/")
			if len(pieces) != 2 {
				return nil, fmt.Errorf("invalid millisecond step %q", part)
			}
			base = pieces[0]
			var err error
			step, err = strconv.Atoi(pieces[1])
			if err != nil || step < 1 || step > 999 {
				return nil, fmt.Errorf("millisecond step must be between 1 and 999")
			}
		}

		start, end := 0, 999
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid millisecond range %q", base)
			}
			var err error
			start, err = strconv.Atoi(bounds[0])
			if err != nil {
				return nil, fmt.Errorf("invalid millisecond range %q", base)
			}
			end, err = strconv.Atoi(bounds[1])
			if err != nil {
				return nil, fmt.Errorf("invalid millisecond range %q", base)
			}
		default:
			value, err := strconv.Atoi(base)
			if err != nil {
				return nil, fmt.Errorf("invalid millisecond value %q", base)
			}
			start, end = value, value
		}
		if start < 0 || end > 999 || start > end {
			return nil, fmt.Errorf("milliseconds must be between 0 and 999")
		}
		for value := start; value <= end; value += step {
			seen[value] = struct{}{}
		}
	}
	values := make([]int, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("millisecond field has no values")
	}
	sort.Ints(values)
	return values, nil
}
