package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const adviceJSON = `{"read":2,"counts":{"cpu_over":1},"modules":[{"name":"shop-api","cpu":{"request":4.45,"p95":1.2345,"suggested_request":2,"confidence":"high"},
"memory":{"request":8589934592,"p95":3221225472,"suggested_request":5368709120,"utilization":0.376}}]}`

func TestTheFiguresOfAnAnswerAreLookedForAmongTheNumbersOfTheTools(t *testing.T) {
	evidence := []string{adviceJSON}
	grounded := []string{
		"CPU Requests 4.45 核, P95 is 1.23 cores, recommended 2.0 cores",
		"内存 Requests 8 GiB，P95 为 3.0GiB，推荐 5 GiB",
		"CPU 4450m now, the recommendation is 2000m",
		"利用率 37.6%，约 38%",
	}
	for _, answer := range grounded {
		assert.Empty(t, UngroundedFigures(answer, evidence), answer)
	}

	// a figure the model made up, or worked out itself, is named
	assert.Equal(t, []string{"7.5 核"}, UngroundedFigures("CPU Requests 4.45 核，建议降到 7.5 核", evidence))
	assert.Equal(t, []string{"512 MiB", "63%"}, UngroundedFigures("内存剩 512 MiB，浪费 63%", evidence), "units are read in their own scale: 512 MiB is not any number of the tools")
	assert.Equal(t, []string{"2.5x"}, UngroundedFigures("可降低到原来的 2.5x", evidence))
}

func TestWhatIsNotAMeasurementIsNotChecked(t *testing.T) {
	evidence := []string{adviceJSON}
	for _, answer := range []string{
		"共读了 3 个模块，2 个超配",                          // counts are written as whole numbers
		"P95 和 P99 的比较，版本 v1.2.3，2026-11-27 08:30", // names, versions, dates, times
		"见 `limit: 12.5` 和 ```\n99.9 GiB\n``` 以及 https://example.com/a/1.5",
	} {
		assert.Empty(t, UngroundedFigures(answer, evidence), answer)
	}
}

func TestAnAnswerWithoutAReadHasNothingToBeCheckedAgainst(t *testing.T) {
	assert.Nil(t, UngroundedFigures("Requests 4.45 核", nil))
	assert.Nil(t, UngroundedFigures("Requests 4.45 核", []string{}))
}

func TestTheNoteNamesTheFiguresInTheLanguageOfTheReader(t *testing.T) {
	assert.Empty(t, ungroundedNote(nil, "zh"))
	assert.Contains(t, ungroundedNote([]string{"7.5 核", "63%"}, "zh"), "未经测量：数字 7.5 核、63%")
	assert.Contains(t, ungroundedNote([]string{"7.5 cores"}, "en"), "Not measured: the figures 7.5 cores")
	many := make([]string, 12)
	for i := range many {
		many[i] = "1.5x"
	}
	assert.Contains(t, ungroundedNote(many, "en"), "…")
}

// The tools a question calls are the evidence: a figure the model quotes from one stands, one it made up is named under the answer.
func TestAnAnswerIsCheckedAgainstWhatTheToolsOfItsQuestionReturned(t *testing.T) {
	s := &Service{}
	byName := map[string]tool{"analyze_resources": {
		spec: ToolSpec{Name: "analyze_resources"},
		run: func(context.Context, json.RawMessage) (toolResult, error) {
			var data any
			require.NoError(t, json.Unmarshal([]byte(adviceJSON), &data))
			return toolResult{Data: data}, nil
		},
	}}
	tr := &trail{}
	ctx := context.WithValue(context.Background(), trailKey{}, tr)
	_, trace, _ := s.runTool(ctx, byName, ToolCall{ID: "1", Name: "analyze_resources", Arguments: "{}"})
	require.True(t, trace.OK)
	require.Len(t, tr.seen(), 1)

	honest := &Reply{Content: "shop-api: Requests 4.45 cores, recommended 2 cores."}
	verifyFigures(honest, tr, "en")
	assert.Empty(t, honest.Unverified)
	assert.NotContains(t, honest.Content, "Not measured")

	invented := &Reply{Content: "shop-api: Requests 4.45 cores, it can drop to 0.8 cores."}
	verifyFigures(invented, tr, "en")
	assert.Equal(t, []string{"0.8 cores"}, invented.Unverified)
	assert.Contains(t, invented.Content, "Not measured: the figures 0.8 cores")

	// a tool that failed gives no evidence
	_, failed, _ := s.runTool(ctx, byName, ToolCall{ID: "2", Name: "no_such_tool", Arguments: "{}"})
	assert.False(t, failed.OK)
	assert.Len(t, tr.seen(), 1)
	verifyFigures(nil, tr, "en")
	verifyFigures(&Reply{Content: "x 1.5 cores"}, nil, "en")
}
