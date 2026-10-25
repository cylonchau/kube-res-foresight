package decision

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC) // a Wednesday

func basePolicy() Policy {
	return Policy{
		Bounds:        Bounds{MinCPUMilli: 100, MaxCPUMilli: 4000, MinMemoryBytes: 128 << 20, MaxMemoryBytes: 8 << 30, MinReplicas: 2, MaxReplicas: 20},
		Fallback:      Fallback{CPUMilli: 500, MemoryBytes: 512 << 20, Replicas: 3},
		HeadroomRatio: 1.2, PodCapacityRPS: 100, TTL: 15 * time.Minute,
	}
}

func inputs() Inputs {
	return Inputs{
		Now: now, Policy: basePolicy(),
		CPUCores:    []Candidate{{Algorithm: "gbdt", Peak: 1.5}},
		MemoryBytes: []Candidate{{Algorithm: "gbdt", Peak: float64(1 << 30)}},
		RPS:         []Candidate{{Algorithm: "gbdt", Peak: 450}},
	}
}

func TestAlgorithmResultIsConvertedWithHeadroom(t *testing.T) {
	rec := Decide(inputs())
	assert.Equal(t, Field{Set: true, Value: 1800, Source: SourceAlgorithm, Detail: "gbdt", Reason: rec.CPUMilli.Reason}, rec.CPUMilli)
	assert.EqualValues(t, math.Ceil(float64(1<<30)*1.2), rec.MemoryBytes.Value)
	assert.EqualValues(t, 6, rec.Replicas.Value, "ceil(450 * 1.2 / 100)")
	assert.Equal(t, now.Add(15*time.Minute), rec.ValidUntil)
}

func TestBypassKeepsEveryOriginalValueAndBeatsEverything(t *testing.T) {
	in := inputs()
	in.Bypass, in.BypassReason = true, "incident 42"
	in.Periods = []FixedPeriod{{Name: "all-day", Weekdays: 0x7f, StartMinute: 0, EndMinute: 1439, CPUMilli: 900}}
	rec := Decide(in)
	for _, f := range []Field{rec.CPUMilli, rec.MemoryBytes, rec.Replicas} {
		assert.False(t, f.Set)
		assert.Equal(t, SourceBypass, f.Source)
		assert.Contains(t, f.Reason, "incident 42")
	}
}

func TestFixedPeriodOverridesOnlyTheFieldsItSets(t *testing.T) {
	in := inputs()
	in.Periods = []FixedPeriod{{Name: "sale", Weekdays: 1 << time.Wednesday, StartMinute: 9 * 60, EndMinute: 12 * 60, Replicas: 10}}
	rec := Decide(in)
	assert.Equal(t, SourceFixedPeriod, rec.Replicas.Source)
	assert.EqualValues(t, 10, rec.Replicas.Value)
	assert.Equal(t, SourceAlgorithm, rec.CPUMilli.Source, "cpu is not set by the period, the model still decides")
	assert.Equal(t, SourceAlgorithm, rec.MemoryBytes.Source)
}

func TestInactivePeriodsAreIgnored(t *testing.T) {
	in := inputs()
	in.Periods = []FixedPeriod{
		{Name: "thursday", Weekdays: 1 << time.Thursday, StartMinute: 0, EndMinute: 1000, Replicas: 10},
		{Name: "afternoon", Weekdays: 1 << time.Wednesday, StartMinute: 13 * 60, EndMinute: 14 * 60, Replicas: 11},
		{Name: "invalid", Weekdays: 0, StartMinute: 0, EndMinute: 100, Replicas: 12},
	}
	assert.Equal(t, SourceAlgorithm, Decide(in).Replicas.Source)
}

func TestPeriodConflictsResolveByPriorityThenName(t *testing.T) {
	in := inputs()
	window := func(name string, prio int, replicas int) FixedPeriod {
		return FixedPeriod{Name: name, Priority: prio, Weekdays: 0x7f, StartMinute: 0, EndMinute: 1439, Replicas: replicas}
	}
	in.Periods = []FixedPeriod{window("b", 0, 8), window("a", 0, 9)}
	rec := Decide(in)
	assert.EqualValues(t, 9, rec.Replicas.Value, "equal priority: the name decides, and the conflict is reported")
	assert.Contains(t, rec.Replicas.Reason, "conflicts")

	in.Periods = []FixedPeriod{window("b", 5, 8), window("a", 0, 9)}
	rec = Decide(in)
	assert.EqualValues(t, 8, rec.Replicas.Value)
	assert.Contains(t, rec.Replicas.Reason, "by priority")
}

func TestOvernightPeriodCrossesMidnightIntoTheNextDay(t *testing.T) {
	p := FixedPeriod{Name: "night", Weekdays: 1 << time.Friday, StartMinute: 22 * 60, EndMinute: 6 * 60, Replicas: 4}
	friday2300 := time.Date(2026, 3, 6, 23, 0, 0, 0, time.UTC)
	saturday0500 := time.Date(2026, 3, 7, 5, 0, 0, 0, time.UTC)
	saturday2300 := time.Date(2026, 3, 7, 23, 0, 0, 0, time.UTC)
	friday0500 := time.Date(2026, 3, 6, 5, 0, 0, 0, time.UTC)
	assert.True(t, p.Active(friday2300, time.UTC))
	assert.True(t, p.Active(saturday0500, time.UTC), "the morning half belongs to the Friday start")
	assert.False(t, p.Active(saturday2300, time.UTC))
	assert.False(t, p.Active(friday0500, time.UTC), "Friday morning belongs to Thursday's window")
}

func TestPeriodsFollowTheWallClockOfTheirZoneAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata not available")
	}
	p := FixedPeriod{Name: "morning", Weekdays: 0x7f, StartMinute: 9 * 60, EndMinute: 10 * 60, Replicas: 4}
	// 09:30 New York is 14:30 UTC in winter (EST) and 13:30 UTC after the March DST change (EDT).
	winter := time.Date(2026, 3, 2, 14, 30, 0, 0, time.UTC)
	summer := time.Date(2026, 3, 10, 13, 30, 0, 0, time.UTC)
	assert.True(t, p.Active(winter, ny))
	assert.True(t, p.Active(summer, ny))
	assert.False(t, p.Active(time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC), ny), "14:30 UTC is 10:30 after DST starts")
}

func TestAbsolutePeriodUsesInstants(t *testing.T) {
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	p := FixedPeriod{Name: "launch", AbsStart: &start, AbsEnd: &end, CPUMilli: 2000}
	assert.True(t, p.Active(now, nil))
	assert.False(t, p.Active(end, nil), "the end instant is exclusive")
	half := FixedPeriod{Name: "half", AbsStart: &start, CPUMilli: 2000}
	assert.False(t, half.Active(now, nil))
	assert.Error(t, ValidatePeriod(half))
}

func TestInvalidPredictionsFallThroughToTheNextCandidateThenFallback(t *testing.T) {
	in := inputs()
	in.CPUCores = []Candidate{{Algorithm: "a", Peak: math.NaN()}, {Algorithm: "b", Peak: -1}, {Algorithm: "c", Peak: math.Inf(1)}, {Algorithm: "d", Peak: 2}}
	rec := Decide(in)
	assert.Equal(t, "d", rec.CPUMilli.Detail)
	assert.EqualValues(t, 2400, rec.CPUMilli.Value)

	in.CPUCores = []Candidate{{Algorithm: "a", Peak: math.NaN()}}
	rec = Decide(in)
	assert.Equal(t, SourceFallback, rec.CPUMilli.Source)
	assert.EqualValues(t, 500, rec.CPUMilli.Value)
	assert.Contains(t, rec.CPUMilli.Reason, "a: invalid prediction")
}

func TestNothingUsableKeepsTheOriginal(t *testing.T) {
	in := inputs()
	in.Policy.Fallback = Fallback{}
	in.CPUCores, in.MemoryBytes, in.RPS = nil, nil, nil
	rec := Decide(in)
	for _, f := range []Field{rec.CPUMilli, rec.MemoryBytes, rec.Replicas} {
		assert.False(t, f.Set)
		assert.Equal(t, SourceOriginal, f.Source)
	}
}

func TestBoundsClampEverySourceExceptBypass(t *testing.T) {
	in := inputs()
	in.CPUCores = []Candidate{{Algorithm: "gbdt", Peak: 50}} // 60000m, over the 4000m maximum
	in.MemoryBytes = []Candidate{{Algorithm: "gbdt", Peak: 1}}
	in.RPS = []Candidate{{Algorithm: "gbdt", Peak: 1}} // 1 replica, below the minimum 2
	rec := Decide(in)
	assert.EqualValues(t, 4000, rec.CPUMilli.Value)
	assert.True(t, rec.CPUMilli.Clamped)
	assert.EqualValues(t, 128<<20, rec.MemoryBytes.Value)
	assert.EqualValues(t, 2, rec.Replicas.Value)

	in.Periods = []FixedPeriod{{Name: "huge", Weekdays: 0x7f, StartMinute: 0, EndMinute: 1439, Replicas: 500}}
	rec = Decide(in)
	assert.EqualValues(t, 20, rec.Replicas.Value, "an administrator window cannot break the hard bound either")
	assert.Equal(t, SourceFixedPeriod, rec.Replicas.Source)
}

func TestReplicasNeedAVerifiedPodCapacity(t *testing.T) {
	in := inputs()
	in.Policy.PodCapacityRPS = 0
	rec := Decide(in)
	assert.Equal(t, SourceFallback, rec.Replicas.Source)
	assert.Contains(t, rec.Replicas.Reason, "pod capacity is not configured")
	in.Policy.Fallback.Replicas = 0
	assert.Equal(t, SourceOriginal, Decide(in).Replicas.Source)
}

func TestZeroTrafficStillYieldsAtLeastOneReplica(t *testing.T) {
	in := inputs()
	in.Policy.MinReplicas = 0
	in.RPS = []Candidate{{Algorithm: "naive", Peak: 0}}
	rec := Decide(in)
	assert.Equal(t, SourceAlgorithm, rec.Replicas.Source)
	assert.EqualValues(t, 1, rec.Replicas.Value)
}

func TestHeadroomBelowOneIsTreatedAsNone(t *testing.T) {
	in := inputs()
	in.Policy.HeadroomRatio = 0.1
	assert.EqualValues(t, 1500, Decide(in).CPUMilli.Value)
}

func TestHugePredictionsAreRejectedNotOverflowed(t *testing.T) {
	in := inputs()
	in.CPUCores = []Candidate{{Algorithm: "bad", Peak: 1e30}}
	rec := Decide(in)
	require.Equal(t, SourceFallback, rec.CPUMilli.Source)
	assert.Contains(t, rec.CPUMilli.Reason, "out of range")
}

func TestValidatePeriod(t *testing.T) {
	good := FixedPeriod{Name: "x", Weekdays: 1, StartMinute: 0, EndMinute: 60, CPUMilli: 1}
	assert.NoError(t, ValidatePeriod(good))
	for name, mutate := range map[string]func(*FixedPeriod){
		"no name":      func(p *FixedPeriod) { p.Name = "" },
		"no values":    func(p *FixedPeriod) { p.CPUMilli = 0 },
		"negative":     func(p *FixedPeriod) { p.Replicas = -1 },
		"no weekday":   func(p *FixedPeriod) { p.Weekdays = 0 },
		"bad weekday":  func(p *FixedPeriod) { p.Weekdays = 0xff },
		"same minutes": func(p *FixedPeriod) { p.EndMinute = p.StartMinute },
		"bad minute":   func(p *FixedPeriod) { p.EndMinute = 2000 },
	} {
		p := good
		mutate(&p)
		assert.Error(t, ValidatePeriod(p), name)
	}
}
