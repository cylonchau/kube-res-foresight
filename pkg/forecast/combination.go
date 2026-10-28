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
// The weights follow the way the platform grades an algorithm: a member's error counts UnderWeight times where it forecast below the load
// (a shortfall costs more than a surplus), the weight is the inverse error to the power Sharpness, and a member whose error is more than
// TrimRatio times the best member's is left out; the newest RecentSteps steps are weighed in as well, so a member that has just followed a
// change counts for more than one that was right an hour ago. So the combination leans on the member that has lately been right and short of nothing,
// instead of averaging a member that has lately been wrong with the one that has not. Zero values keep the plain inverse mean absolute error.
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
	// UnderWeight counts a shortfall of a member this many times when its error is measured; below 1 = 1.
	UnderWeight float64
	// Sharpness is the power of the inverse error that is the weight; below 1 = 1.
	Sharpness float64
	// TrimRatio leaves out the members whose error exceeds this many times the best one's; 0 = none.
	TrimRatio float64
	// RecentSteps adds the newest steps to the span the member errors are measured on.
	RecentSteps int
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
	if m.UnderWeight < 0 || m.Sharpness < 0 || m.RecentSteps < 0 || m.TrimRatio < 0 || (m.TrimRatio > 0 && m.TrimRatio < 1) {
		return fmt.Errorf("under_weight, sharpness and recent_steps must not be negative, and trim_ratio is 0 (none) or at least 1")
	}
	return nil
}

// memberError is the mean absolute error (a shortfall counted UnderWeight times) a member made at each of the last windows horizons before
// the end of the history and over the newest RecentSteps steps, and whether it could be measured at all.
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
			e := math.Abs(v - in.Values[c+h])
			if v < in.Values[c+h] && m.UnderWeight > 1 {
				e *= m.UnderWeight // a shortfall costs more than a surplus
			}
			sum += e
			count++
		}
	}
	// the newest steps count as well: a member that has just followed a change is worth more than one that was right last hour
	for r := 1; r <= m.RecentSteps; r++ {
		c := n - r
		if c < need {
			break
		}
		got, err := member.Forecast(ctx, cut(in, c), r)
		if err != nil || len(got) != r {
			continue
		}
		for h, v := range got {
			e := math.Abs(v - in.Values[c+h])
			if v < in.Values[c+h] && m.UnderWeight > 1 {
				e *= m.UnderWeight
			}
			sum += e
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
		errs := make([]float64, len(ran))
		best := math.Inf(1)
		for i, r := range ran {
			if e, ok := m.memberError(ctx, r.model, in, horizon); ok {
				errs[i] = e
				measured[i] = 1 // a marker until the weights are made below
				best = math.Min(best, e)
			}
		}
		sharp := math.Max(m.Sharpness, 1)
		var sum float64
		var count int
		for i := range ran {
			if measured[i] == 0 {
				continue
			}
			if m.TrimRatio >= 1 && errs[i] > m.TrimRatio*best+eps {
				measured[i] = -1 // clearly worse than the best member lately: left out
				continue
			}
			measured[i] = math.Pow(1/(errs[i]+eps), sharp)
			sum += measured[i]
			count++
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count > 0 {
			mean := sum / float64(count)
			for i := range weights {
				switch {
				case measured[i] > 0:
					weights[i] = measured[i]
				case measured[i] < 0:
					weights[i] = 0
				default:
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

// WithShortTerm is the combination whose members that remember long (a percentile, a seasonal profile, Holt-Winters ...) are put behind the
// short-term correction, so the combination sees a burst as quickly as its quickest member.
func (m Combination) WithShortTerm(cfg ShortTermConfig) Combination {
	if !cfg.Enabled() {
		return m
	}
	members := make([]Model, len(m.Members))
	for i, member := range m.Members {
		members[i] = member
		if longMemory[member.ID()] {
			members[i] = ShortTerm{Inner: member, Cfg: cfg}
		}
	}
	m.Members = members
	return m
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
