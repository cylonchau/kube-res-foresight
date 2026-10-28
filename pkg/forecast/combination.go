package forecast

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Combination is the forecast of several algorithms put together: the median of what they forecast for each step, or their
// average weighted by how well each did on this workload lately (the weight is the inverse of the mean absolute error the member
// made over the last Windows horizons of the very history it is given). A combination of unlike models is hard to beat in the
// forecasting competitions (M4): a member that is wrong in one way is outvoted by the others, and it costs no training.
//
// The members are named in the algorithm's own parameter (`members`, a list of ids) and run with their default parameters, so only
// an algorithm that needs no required parameter can be one (ewma, holt_winters, theta, hist_percentile, naive, knn). That is the
// difference from the idea of combining "the promoted algorithms": the platform's promotion and the parameters set on the
// algorithm page are not read here. A combination is a model like any other, so it is backtested, scored and promoted by the same
// rules as the algorithms in it, which is how it earns its place instead of being assumed better.
//
// Only values before the forecast origin are used: the weights come from cut-offs of the history it is handed. A member that
// cannot run on the history (it needs more of it) is left out of that forecast; with none left the combination is skipped, with
// the same reason a single algorithm would give.
type Combination struct {
	Members []Model
	Mode    string // "inverse_error" or "median"
	Windows int    // horizons the member errors are measured over; 0 = 4
}

func (Combination) ID() string { return "combination" }

// defaultCombinationMembers are the members when none are named: three unlike statistical models that need no parameter.
const defaultCombinationMembers = "ewma,holt_winters,theta"

const (
	combineInverseError = "inverse_error"
	combineMedian       = "median"
	// defaultCombinationWindows is how many horizons the weights are measured over.
	defaultCombinationWindows = 4
)

func (m Combination) windows() int {
	if m.Windows > 0 {
		return m.Windows
	}
	return defaultCombinationWindows
}

// MinSamples is the shortest of the members: a member that needs more is left out, not waited for.
func (m Combination) MinSamples() int {
	min := math.MaxInt
	for _, member := range m.Members {
		if v := member.MinSamples(); v < min {
			min = v
		}
	}
	if min == math.MaxInt {
		return 1
	}
	return min
}

// MinSamplesFor is the shortest of the members for the horizon.
func (m Combination) MinSamplesFor(horizon int) int {
	min := math.MaxInt
	for _, member := range m.Members {
		if v := MinSamplesFor(member, horizon); v < min {
			min = v
		}
	}
	if min == math.MaxInt {
		return 1
	}
	return min
}

func (m Combination) validate() error {
	if len(m.Members) < 2 {
		return fmt.Errorf("a combination needs at least 2 members")
	}
	if m.Mode != "" && m.Mode != combineInverseError && m.Mode != combineMedian {
		return fmt.Errorf("mode must be inverse_error or median")
	}
	if m.Windows < 0 {
		return fmt.Errorf("error_windows must not be negative")
	}
	return nil
}

// memberErrors is the mean absolute error a member made at each of the last windows horizons before the end of the history, and
// whether it could be measured at all.
func (m Combination) memberError(ctx context.Context, member Model, in Input, horizon int) (float64, bool) {
	n := len(in.Values)
	need := MinSamplesFor(member, horizon)
	var sum float64
	var count int
	for j := 1; j <= m.windows(); j++ {
		c := n - j*horizon
		if c < need {
			break
		}
		got, err := member.Forecast(ctx, cut(in, c), horizon)
		if err != nil || len(got) != horizon {
			continue
		}
		for h, v := range got {
			sum += math.Abs(v - in.Values[c+h])
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

func (m Combination) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	type member struct {
		model  Model
		values []float64
	}
	var ran []member
	var reasons []string
	for _, candidate := range m.Members {
		if n < MinSamplesFor(candidate, horizon) {
			reasons = append(reasons, fmt.Sprintf("%s needs %d", candidate.ID(), MinSamplesFor(candidate, horizon)))
			continue
		}
		got, err := candidate.Forecast(ctx, in, horizon)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil || len(got) != horizon {
			reasons = append(reasons, candidate.ID()+" failed")
			continue
		}
		ran = append(ran, member{candidate, got})
	}
	if len(ran) == 0 {
		return nil, fmt.Errorf("%w: no member of the combination can forecast on %d samples (%s)", ErrInsufficientHistory, n, strings.Join(reasons, "; "))
	}

	weights := make([]float64, len(ran))
	for i := range weights {
		weights[i] = 1
	}
	if m.Mode != combineMedian && len(ran) > 1 {
		var level float64
		for _, v := range in.Values {
			level += math.Abs(v)
		}
		level /= float64(n)
		eps := 1e-3*level + 1e-12 // a member that was exactly right does not take all the weight
		measured := make([]float64, len(ran))
		var sum float64
		var count int
		for i, r := range ran {
			if e, ok := m.memberError(ctx, r.model, in, horizon); ok {
				measured[i] = 1 / (e + eps)
				sum += measured[i]
				count++
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count > 0 {
			mean := sum / float64(count)
			for i := range weights {
				if measured[i] > 0 {
					weights[i] = measured[i]
				} else {
					weights[i] = mean // a member that could not be measured counts as an average one
				}
			}
		}
	}

	out := make([]float64, horizon)
	for h := range out {
		if m.Mode == combineMedian {
			col := make([]float64, len(ran))
			for i, r := range ran {
				col[i] = r.values[h]
			}
			out[h] = medianOf(col)
			continue
		}
		var num, den float64
		for i, r := range ran {
			num += weights[i] * r.values[h]
			den += weights[i]
		}
		out[h] = num / den
	}
	return out, finite(out)
}

// ParseMembers reads the list of member ids ("ewma, holt_winters, theta") and builds each with its default parameters. An id that
// is not an algorithm, one that needs a parameter, the combination itself, and a repeat are refused.
func ParseMembers(list string) ([]Model, error) {
	var ids []string
	for _, part := range strings.Split(list, ",") {
		if id := strings.TrimSpace(part); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 || len(ids) > 6 {
		return nil, fmt.Errorf("members must name 2 to 6 algorithms, got %d", len(ids))
	}
	seen := map[string]bool{}
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		if id == "combination" {
			return nil, errors.New("a combination cannot be a member of itself")
		}
		if seen[id] {
			return nil, fmt.Errorf("member %q is named twice", id)
		}
		seen[id] = true
		model, err := Build(id, map[string]any{})
		if err != nil {
			return nil, fmt.Errorf("member %q cannot be used with its default parameters: %v", id, err)
		}
		out = append(out, model)
	}
	return out, nil
}
