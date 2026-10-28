package forecast

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// constant forecasts a flat line; the id is what the combination and the short-term correction know it by.
type constant struct {
	id    string
	level float64
}

func (c constant) ID() string      { return c.id }
func (c constant) MinSamples() int { return 1 }
func (c constant) Forecast(_ context.Context, in Input, horizon int) ([]float64, error) {
	if err := checkInput(c, in, horizon); err != nil {
		return nil, err
	}
	out := make([]float64, horizon)
	for i := range out {
		out[i] = c.level
	}
	return out, nil
}

func flat(n int, v float64) Input {
	values := make([]float64, n)
	for i := range values {
		values[i] = v
	}
	return Input{Values: values, Step: time.Minute, End: shortTermEnd}
}

// The combination leans on the member that has lately been right, drops one that is clearly worse, and counts a shortfall more than a surplus.
func TestTheCombinationLeansOnTheMemberThatHasBeenRightAndLeavesOutTheWorst(t *testing.T) {
	ctx := context.Background()
	in := flat(200, 10)
	right, near, far := constant{"right", 10}, constant{"near", 11}, constant{"far", 40}

	plain := Combination{Members: []Model{right, near, far}, Mode: "inverse_error"}
	sharp := Combination{Members: []Model{right, near, far}, Mode: "inverse_error", Sharpness: 3}
	trimmed := Combination{Members: []Model{right, near, far}, Mode: "inverse_error", Sharpness: 3, TrimRatio: 3}

	p, err := plain.Forecast(ctx, in, 3)
	require.NoError(t, err)
	s, err := sharp.Forecast(ctx, in, 3)
	require.NoError(t, err)
	tr, err := trimmed.Forecast(ctx, in, 3)
	require.NoError(t, err)
	assert.Greater(t, p[0], s[0], "a higher power leans harder on the member that is right: the forecast comes closer to 10")
	assert.Greater(t, s[0], tr[0], "and the members that are off are left out altogether")
	assert.InDelta(t, 10, tr[0], 0.05, "the best member was exact, so anything off by more than three times its (nil) error is out")

	// a shortfall counts more: of two members equally far from the load, the one that forecast too much wins
	load := flat(200, 10)
	low, high := constant{"low", 8}, constant{"high", 12}
	sym := Combination{Members: []Model{low, high}, Mode: "inverse_error", Sharpness: 4}
	asym := Combination{Members: []Model{low, high}, Mode: "inverse_error", Sharpness: 4, UnderWeight: 3}
	a, err := sym.Forecast(ctx, load, 2)
	require.NoError(t, err)
	b, err := asym.Forecast(ctx, load, 2)
	require.NoError(t, err)
	assert.InDelta(t, 10, a[0], 1e-6, "equal errors, equal weights")
	assert.Greater(t, b[0], 10.0, "the member that is short is trusted less")

	assert.Error(t, Combination{Members: []Model{low, high}, TrimRatio: 0.5}.validate())
	assert.Error(t, Combination{Members: []Model{low, high}, RecentSteps: -1}.validate())
}

// The newest steps are weighed in: a member that just followed a rise outweighs one that was right before it.
func TestTheCombinationTrustsTheMemberThatHasJustFollowedAChange(t *testing.T) {
	ctx := context.Background()
	values := make([]float64, 0, 205)
	for i := 0; i < 200; i++ {
		values = append(values, 1)
	}
	values = append(values, 9, 9, 9, 9, 9) // a step up in the last five minutes
	in := Input{Values: values, Step: time.Minute, End: shortTermEnd}
	old, now := constant{"old", 1}, constant{"now", 9}
	without := Combination{Members: []Model{old, now}, Mode: "inverse_error", Windows: 2, Sharpness: 2}
	with := Combination{Members: []Model{old, now}, Mode: "inverse_error", Windows: 2, Sharpness: 2, RecentSteps: 5}
	a, err := without.Forecast(ctx, in, 5)
	require.NoError(t, err)
	b, err := with.Forecast(ctx, in, 5)
	require.NoError(t, err)
	assert.Greater(t, b[0], a[0], "the member that matches the newest steps weighs more")
	assert.Greater(t, b[0], 8.0)
}

func TestACombinationPutsOnlyItsMembersThatRememberLongBehindTheShortTermCorrection(t *testing.T) {
	members := []Model{constant{"ewma", 1}, constant{"hist_percentile", 2}, constant{"seasonal_profile", 3}}
	got := Combination{Members: members}.WithShortTerm(DefaultShortTerm())
	_, firstWrapped := got.Members[0].(ShortTerm)
	_, secondWrapped := got.Members[1].(ShortTerm)
	_, thirdWrapped := got.Members[2].(ShortTerm)
	assert.False(t, firstWrapped, "ewma follows a change on its own")
	assert.True(t, secondWrapped)
	assert.True(t, thirdWrapped)
	assert.Equal(t, []string{"ewma", "hist_percentile", "seasonal_profile"}, []string{got.Members[0].ID(), got.Members[1].ID(), got.Members[2].ID()})
	assert.Equal(t, members[1], Combination{Members: members}.WithShortTerm(ShortTermConfig{}).Members[1], "off: nothing is wrapped")
}

// The new parameters have defaults that are not stored, and the implementation version moved: what was promoted under the old weighting
// has to be backtested again.
func TestTheCombinationWeightParametersAreNotStoredAtTheirDefaultsAndTheVersionMoved(t *testing.T) {
	def, ok := Lookup("combination")
	require.True(t, ok)
	assert.Equal(t, "2", def.ImplVersion)
	normalized, err := Normalize("combination", map[string]any{})
	require.NoError(t, err)
	stored := Canonical("combination", normalized)
	for _, name := range []string{"under_weight", "sharpness", "trim_ratio", "recent_steps"} {
		assert.Contains(t, normalized, name)
		assert.NotContains(t, stored, name, name)
	}
	m, err := Build("combination", map[string]any{})
	require.NoError(t, err)
	c, ok := m.(Combination)
	require.True(t, ok)
	assert.EqualValues(t, 2, c.UnderWeight)
	assert.EqualValues(t, 2, c.Sharpness)
	assert.EqualValues(t, 3, c.TrimRatio)
	assert.Equal(t, 3, c.RecentSteps)
}
