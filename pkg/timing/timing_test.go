package timing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A stored value is used as it is inside the range, brought to the nearest end outside it, and replaced by the default when it is missing
// or not a number: the program never runs on a limit nobody could have meant.
func TestAStoredLimitIsKeptInItsRangeOrFallsBackToTheDefault(t *testing.T) {
	p := OperatorRequestTimeout
	assert.Equal(t, 25, p.Parse("25"))
	assert.Equal(t, 25, p.Parse(" 25 "))
	assert.Equal(t, p.Max, p.Parse("100000"))
	assert.Equal(t, p.Min, p.Parse("0"))
	assert.Equal(t, p.Min, p.Parse("-5"))
	assert.Equal(t, p.Default, p.Parse(""))
	assert.Equal(t, p.Default, p.Parse("soon"))
	assert.Equal(t, p.Default, p.Parse("1.5"))
	assert.Equal(t, 0, OperatorDecisionCacheTTL.Parse("0"), "0 is a real value where the range allows it: the cache is off")
}

func TestEveryLimitHasAKeyADefaultInsideItsRangeAndText(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		assert.NotEmpty(t, p.Key)
		assert.False(t, seen[p.Key], "%s is listed twice", p.Key)
		seen[p.Key] = true
		assert.GreaterOrEqual(t, p.Default, p.Min, p.Key)
		assert.LessOrEqual(t, p.Default, p.Max, p.Key)
		assert.NotEmpty(t, p.Name, p.Key)
		assert.NotEmpty(t, p.Remark, p.Key)
	}
	assert.Len(t, seen, 51)
}

func TestOperatorSettingsAreReadFromTheStoredTextAndNormalized(t *testing.T) {
	stored := map[string]string{"operator.request_timeout_seconds": "20", "operator.sync_interval_seconds": "1", "operator.recover_answers": "x"}
	got := LoadOperator(func(key string) (string, bool) { v, ok := stored[key]; return v, ok })
	assert.Equal(t, 20, got.RequestTimeoutSeconds)
	assert.Equal(t, OperatorSyncInterval.Min, got.SyncIntervalSeconds, "below the range: the lowest allowed")
	assert.Equal(t, OperatorRecoverAnswers.Default, got.RecoverAnswers, "not a number: the default")
	assert.Equal(t, DefaultOperatorSettings().ModuleCacheTTLSeconds, got.ModuleCacheTTLSeconds, "not stored: the default")

	wild := OperatorSettings{RequestTimeoutSeconds: 9999, SyncIntervalSeconds: -1, DecisionCacheTTLSeconds: -1, ModuleCacheTTLSeconds: 1 << 40, RecoverAnswers: 0, SettingsRefreshSeconds: 1}
	n := wild.Normalize()
	assert.Equal(t, OperatorRequestTimeout.Max, n.RequestTimeoutSeconds)
	assert.Equal(t, OperatorSyncInterval.Min, n.SyncIntervalSeconds)
	assert.Equal(t, 0, n.DecisionCacheTTLSeconds)
	assert.Equal(t, OperatorModuleCacheTTL.Max, n.ModuleCacheTTLSeconds)
	assert.Equal(t, OperatorRecoverAnswers.Min, n.RecoverAnswers)
	assert.Equal(t, OperatorSettingsRefresh.Min, n.SettingsRefreshSeconds)
}

// The budget of a request that is answered in one go stays inside the gateway timeout, with the margin kept.
func TestABudgetStaysInsideTheGatewayTimeout(t *testing.T) {
	s := func(n int) time.Duration { return time.Duration(n) * time.Second }
	assert.Equal(t, s(30), Under(s(60), s(30)), "a budget that fits is kept")
	assert.Equal(t, s(55), Under(s(60), s(300)), "a larger one is brought to the gateway timeout less the margin")
	assert.Equal(t, s(55), Under(s(60), 0), "no budget is the room that is left")
	assert.Equal(t, s(295), Under(s(300), s(300)), "a raised gateway timeout gives more room")
	assert.Equal(t, s(5), Under(s(10), s(60)), "a very small gateway timeout still leaves the margin")
	assert.Equal(t, s(5), Under(s(3), s(60)))
}
