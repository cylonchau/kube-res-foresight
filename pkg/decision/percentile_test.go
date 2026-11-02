package decision

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTheLeadIsMadeAtTheLevelThatWasAsked(t *testing.T) {
	ready := []float64{30, 40, 50, 60, 70, 80, 90, 100, 110, 120}
	late := []float64{10, 20, 30, 40, 50}
	at := func(q float64) Lead {
		return ComputeLead(LeadInputs{ReadySeconds: ready, LateSeconds: late, Percentile: q, StepSeconds: 1, HorizonSeconds: 3600})
	}
	def, p40, p41, p99 := at(0), at(0.40), at(0.41), at(0.99)
	assert.InDelta(t, Percentile(ready, 0.9), def.ReadySeconds, 1e-9, "no level asked: the default, P90")
	assert.InDelta(t, 0.9, def.Percentile, 1e-12)
	assert.InDelta(t, Percentile(ready, 0.40), p40.ReadySeconds, 1e-9)
	assert.InDelta(t, Percentile(late, 0.40), p40.MarginSeconds, 1e-9, "the lateness margin is taken at the same level")
	assert.Greater(t, p41.Seconds, p40.Seconds, "P41 is not P40")
	assert.Greater(t, p99.Seconds, def.Seconds)
	assert.Contains(t, p40.Reason, "P40 of 10 measured ready times")
	assert.Contains(t, p99.Reason, "P99 of 10")
}

func TestTheDelayIndicatorsAreJudgedAtTheLevelThatWasAsked(t *testing.T) {
	in := DelayInputs{Timely: 5, Late: 5, LateSeconds: []float64{10, 20, 30, 40, 50}, PeakUnder: []float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.09, 0.10}}
	find := func(q float64, key string) Indicator {
		in.Percentile = q
		for _, i := range DelayIndicators(in) {
			if i.Key == key {
				return i
			}
		}
		t.Fatalf("no indicator %s", key)
		return Indicator{}
	}
	assert.InDelta(t, Percentile(in.LateSeconds, 0.9), find(0, "late_p90_seconds").Value, 1e-9, "default P90")
	late := find(0.42, "late_p90_seconds")
	assert.InDelta(t, Percentile(in.LateSeconds, 0.42), late.Value, 1e-9)
	assert.InDelta(t, 0.42, late.Percentile, 1e-12, "the indicator says which level it is")
	assert.Contains(t, late.Note, "P42")
	peak := find(0.42, "peak_under_p90")
	assert.InDelta(t, Percentile(in.PeakUnder, 0.42), peak.Value, 1e-9)
	assert.Contains(t, peak.Note, "P42")
	assert.Zero(t, find(0.42, "timely_rate").Percentile, "an indicator that is not a percentile has none")
}

func TestPercentName(t *testing.T) {
	for q, want := range map[float64]string{0.9: "P90", 0.4: "P40", 0.995: "P99.5", 0.01: "P1", 0.999: "P99.9"} {
		assert.Equal(t, want, PercentName(q))
	}
}
