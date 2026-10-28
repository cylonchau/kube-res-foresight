package forecast

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cost of a forecast on the largest history a one minute dataset holds in the default retention (30 days, a day of 1440
// steps and a week of 10080): the decomposition is linear in the history, so it must stay far below the training budget.
func TestMSTLCostOnAMonthOfOneMinuteSteps(t *testing.T) {
	n := 30 * 1440
	v := make([]float64, n)
	for i := range v {
		v[i] = 100 + 30*math.Sin(2*math.Pi*float64(i%1440)/1440) + float64((i/1440)%7)
	}
	in := Input{Values: v, Step: time.Minute, End: t0.Add(time.Duration(n-1) * time.Minute)}
	started := time.Now()
	_, err := MSTL{Daily: 1440, Weekly: 10080}.Forecast(context.Background(), in, 60)
	require.NoError(t, err)
	took := time.Since(started)
	t.Logf("MSTL on %d steps with a daily and a weekly season: %s", n, took)
	assert.Less(t, took, 2*time.Second)
}
