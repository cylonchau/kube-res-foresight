package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What one forecast costs on the largest history a one minute dataset keeps (30 days): none of the four trains, so each must stay
// far below the time budget of a run.
func TestTheNewAlgorithmsAreCheapOnAMonthOfOneMinuteSteps(t *testing.T) {
	n := 30 * 1440
	in := atStep(monday, time.Minute, n, func(i int, _ time.Time) float64 {
		return 100 + 30*math.Sin(2*math.Pi*float64(i%1440)/1440) + 0.001*float64(i) + noise(i)
	})
	members, err := ParseMembers("ewma,holt_winters,theta")
	require.NoError(t, err)
	for name, m := range map[string]Model{
		"seasonal_profile": SeasonalProfile{Percentile: 90, BucketMinutes: 60, ByWeekday: true, HalfLifeDays: 14, MinPerBucket: 6},
		"peak_envelope":    PeakEnvelope{EnvelopeWindow: 30},
		"combination":      Combination{Members: members, Mode: "inverse_error"},
		"regime reset":     RegimeReset{Base: Theta{}, Threshold: 8, MinShift: 0.2},
	} {
		started := time.Now()
		_, err := m.Forecast(context.Background(), in, 60)
		require.NoError(t, err, name)
		took := time.Since(started)
		t.Logf("%s on %d steps: %s", name, n, took)
		assert.Less(t, took, 3*time.Second, name)
	}
}
