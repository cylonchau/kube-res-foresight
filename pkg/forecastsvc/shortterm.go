package forecastsvc

import (
	"context"
	"strconv"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// System parameters of the short-term correction of the algorithms that remember long (see forecast.ShortTerm).
const (
	// ConfigShortTermMinutes is the newest stretch compared with the long-period forecast; 0 turns the correction off.
	ConfigShortTermMinutes = "forecast.short_term_recent_minutes"
	// ConfigShortTermTrigger is the share of the recent level the gap has to exceed before it is corrected.
	ConfigShortTermTrigger = "forecast.short_term_trigger"
	// ConfigShortTermFade is the half-life, in minutes, of the correction along the horizon.
	ConfigShortTermFade = "forecast.short_term_fade_minutes"
	// ConfigShortTermDown is the share of a downward gap that is followed (0 = never).
	ConfigShortTermDown = "forecast.short_term_down_share"
)

// shortTermSettings reads the parameters; a missing or invalid value is replaced by its default.
func (s *Service) shortTermSettings(ctx context.Context) forecast.ShortTermConfig {
	out := forecast.DefaultShortTerm()
	if s.Config == nil {
		return out
	}
	num := func(key string, lo, hi float64, dst *float64) {
		v, err := s.Config.GetValueOrDefault(ctx, key, "")
		if err != nil || v == "" {
			return
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= lo && f <= hi {
			*dst = f
		}
	}
	num(ConfigShortTermMinutes, 0, 120, &out.RecentMinutes)
	num(ConfigShortTermTrigger, 0.01, 5, &out.Trigger)
	num(ConfigShortTermFade, 1, 1440, &out.FadeMinutes)
	num(ConfigShortTermDown, 0, 1, &out.DownShare)
	return out
}

// shortTermOf is the correction an instance runs with: the one set on it, for an algorithm that remembers long and for a combination (whose
// members that remember long get it); off for the others.
func shortTermOf(b *model.AlgorithmRun, def forecast.Definition) forecast.ShortTermConfig {
	combination := def.ID == "combination" || def.Base == "combination"
	if b.ShortTermRecentMinutes <= 0 || !(forecast.LongMemory(def) || combination) {
		return forecast.ShortTermConfig{}
	}
	return forecast.ShortTermConfig{RecentMinutes: b.ShortTermRecentMinutes, Trigger: b.ShortTermTrigger, FadeMinutes: b.ShortTermFadeMinutes, DownShare: b.ShortTermDownShare}
}
