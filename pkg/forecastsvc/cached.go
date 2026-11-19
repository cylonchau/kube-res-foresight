package forecastsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/resultcache"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The reports that take long to work out are kept for the next request that asks the same (P11 B3, see package resultcache). What decides whether a kept answer
// is used is the version of the data it was made from, never only its age: the history of the module, what the platform has written about it (forecast runs,
// recommendations, scalings, evaluations), the settings of the module and its sources, and the algorithms. A report that looks at a window that moves with the clock
// has the window in its version too, in steps small enough that the window has moved by a few percent at most.

// previewBucket is how often the replay of a change is made again for the sliding of the history it reads, whatever else holds.
const previewBucket = 10 * time.Minute

// statsBucket is how long the quantiles read from Prometheus are kept: that data changes all the time, so the answer is for this long at most and says how old it is.
const statsBucket = 5 * time.Minute

func (s *Service) cache() *resultcache.Cache {
	if s.Cache != nil {
		return s.Cache
	}
	return resultcache.Default()
}

// cacheTTL is how long an answer is kept (cache.result_minutes); 0 is no cache.
func (s *Service) cacheTTL(ctx context.Context) time.Duration {
	return time.Duration(s.Count(ctx, timing.ResultCache)) * time.Minute
}

// DataVersion is one text that changes whenever anything a report of the module is made from does.
func (s *Service) DataVersion(ctx context.Context, m *model.ForecastModule) (string, error) {
	sample, err := s.Samples.LatestForModule(ctx, m.ID)
	if err != nil {
		return "", err
	}
	activity, err := s.Predictions.ActivityStamp(ctx, m.ID)
	if err != nil {
		return "", err
	}
	algorithms := ""
	if s.Algorithms != nil {
		if algorithms, err = s.Algorithms.Stamp(ctx); err != nil {
			return "", err
		}
	}
	sources, err := s.Modules.Sources(ctx, m.ID)
	if err != nil {
		return "", err
	}
	sourceStamp := make([]int64, 0, len(sources))
	for _, src := range sources {
		sourceStamp = append(sourceStamp, int64(src.ID)^src.UpdatedAt.UnixNano())
	}
	raw, _ := json.Marshal(sourceStamp)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("m%d@%d|s%d|%s|a%s|c%s", m.ID, m.UpdatedAt.UnixNano(), sample, activity, algorithms, hex.EncodeToString(sum[:6])), nil
}

// windowBucket is the number of the stretch of the clock a report over the last hours belongs to: the stretch is the window divided by 48, between a minute and a
// quarter of an hour, so a report is made again when its window has moved by about two percent.
func windowBucket(now time.Time, hours int) int64 {
	step := time.Duration(hours) * time.Hour / 48
	if step < time.Minute {
		step = time.Minute
	}
	if step > 15*time.Minute {
		step = 15 * time.Minute
	}
	return now.Unix() / int64(step/time.Second)
}

func (s *Service) versionOf(ctx context.Context, moduleID uint, extra string) (string, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return "", err
	}
	v, err := s.DataVersion(ctx, m)
	if err != nil {
		return "", err
	}
	return v + "|" + extra, nil
}

// EffectReportCached is EffectReportParts with its answer kept for the next request of the same module, window and parts, and shared with the requests that come
// while it is being worked out. The answer is shared: it is not to be changed.
func (s *Service) EffectReportCached(ctx context.Context, moduleID uint, hours int, parts []string) (*EffectReport, resultcache.Result, error) {
	if hours <= 0 {
		hours = DefaultDelayHours
	}
	if hours > MaxEffectHours {
		hours = MaxEffectHours
	}
	ttl := s.cacheTTL(ctx)
	version, err := s.versionOf(ctx, moduleID, fmt.Sprintf("w%d", windowBucket(s.now(), hours)))
	if err != nil {
		return nil, resultcache.Result{Source: resultcache.Bypassed}, err
	}
	return resultcache.Do(ctx, s.cache(), fmt.Sprintf("effect|%d|%d|%s", moduleID, hours, strings.Join(parts, ",")), version, ttl, func(ctx context.Context) (*EffectReport, error) {
		return s.EffectReportParts(ctx, moduleID, hours, parts)
	})
}

// ResourceAnalysisCached is ResourceAnalysis of one module with its answer kept. An analysis of every module is not kept: its version would be that of all of them.
func (s *Service) ResourceAnalysisCached(ctx context.Context, moduleID uint, windowDays int) (*ResourceAnalysis, resultcache.Result, error) {
	if moduleID == 0 {
		out, err := s.ResourceAnalysis(ctx, moduleID, windowDays)
		return out, resultcache.Result{Source: resultcache.Bypassed}, err
	}
	version, err := s.versionOf(ctx, moduleID, fmt.Sprintf("w%d", windowBucket(s.now(), max(windowDays, 1)*24)))
	if err != nil {
		return nil, resultcache.Result{Source: resultcache.Bypassed}, err
	}
	return resultcache.Do(ctx, s.cache(), fmt.Sprintf("analysis|%d|%d", moduleID, windowDays), version, s.cacheTTL(ctx), func(ctx context.Context) (*ResourceAnalysis, error) {
		return s.ResourceAnalysis(ctx, moduleID, windowDays)
	})
}

func previewKey(moduleID uint, hours int, ch PreviewChange) string {
	raw, _ := json.Marshal(ch)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("preview|%d|%d|%s", moduleID, hours, hex.EncodeToString(sum[:10]))
}

func (s *Service) previewVersion(ctx context.Context, moduleID uint) (string, error) {
	return s.versionOf(ctx, moduleID, fmt.Sprintf("p%d", s.now().Unix()/int64(previewBucket/time.Second)))
}

// PreviewChangeCached is PreviewChange with its answer kept: the replay of the same change over the same hours, with nothing changed in the history, the settings or
// the algorithms, is the same replay.
func (s *Service) PreviewChangeCached(ctx context.Context, moduleID uint, hours int, ch PreviewChange) (*PreviewReport, resultcache.Result, error) {
	if hours <= 0 {
		hours = DefaultPreviewHours
	}
	version, err := s.previewVersion(ctx, moduleID)
	if err != nil {
		return nil, resultcache.Result{Source: resultcache.Bypassed}, err
	}
	return resultcache.Do(ctx, s.cache(), previewKey(moduleID, hours, ch), version, s.cacheTTL(ctx), func(ctx context.Context) (*PreviewReport, error) {
		return s.PreviewChange(ctx, moduleID, hours, ch)
	})
}

// PreviewKept is the answer of a preview that is kept and still good, without working anything out.
func (s *Service) PreviewKept(ctx context.Context, moduleID uint, hours int, ch PreviewChange) (*PreviewReport, bool) {
	if hours <= 0 {
		hours = DefaultPreviewHours
	}
	version, err := s.previewVersion(ctx, moduleID)
	if err != nil {
		return nil, false
	}
	return resultcache.Peek[*PreviewReport](s.cache(), previewKey(moduleID, hours, ch), version, s.cacheTTL(ctx))
}

// PrometheusStatsCached is PrometheusStats with its answer kept for a few minutes: the quantiles are asked of Prometheus, whose data changes all the time, so the
// answer is not tied to a version of the data but to the time, and the page says how old it is.
func (s *Service) PrometheusStatsCached(ctx context.Context, moduleID uint, windowMinutes int, from, to time.Time) (*PromStats, resultcache.Result, error) {
	ttl := min(s.cacheTTL(ctx), statsBucket)
	key := fmt.Sprintf("promstats|%d|%d|%d|%d", moduleID, windowMinutes, from.Unix()/60, to.Unix()/60)
	version := fmt.Sprintf("b%d", s.now().Unix()/int64(statsBucket/time.Second))
	return resultcache.Do(ctx, s.cache(), key, version, ttl, func(ctx context.Context) (*PromStats, error) {
		return s.PrometheusStats(ctx, moduleID, windowMinutes, from, to)
	})
}
