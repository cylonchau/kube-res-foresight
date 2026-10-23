package job

import (
	"testing"
	"time"
)

func TestParseCronMilliseconds(t *testing.T) {
	schedule, err := ParseCron("*/100 * * * * * *")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 50_000_000, time.UTC)
	if got, want := schedule.Next(base), base.Truncate(time.Second).Add(100*time.Millisecond); !got.Equal(want) {
		t.Fatalf("Next() = %v, want %v", got, want)
	}
	if got, want := schedule.Next(base.Truncate(time.Second).Add(950*time.Millisecond)), base.Truncate(time.Second).Add(time.Second); !got.Equal(want) {
		t.Fatalf("next second = %v, want %v", got, want)
	}
}

func TestParseCronEveryMilliseconds(t *testing.T) {
	schedule, err := ParseCron("@every 25ms")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got := schedule.Next(now).Sub(now); got != 25*time.Millisecond {
		t.Fatalf("duration = %v", got)
	}
}

func TestParseCronRejectsInvalidMilliseconds(t *testing.T) {
	for _, expr := range []string{"1000 * * * * * *", "*/0 * * * * * *", "@every 500us"} {
		if _, err := ParseCron(expr); err == nil {
			t.Fatalf("expected %q to fail", expr)
		}
	}
}
