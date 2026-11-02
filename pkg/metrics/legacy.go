package metrics

import (
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

// The metrics were named kube_resource_fcst_* before the project was renamed, and kube_res_foresight_* since. The alerts and dashboards of
// a production system are written with the name they were made with, and they stop firing without a word when the name changes. While they
// are being moved, metrics.legacy_names exports every series under both names; the old one is a copy made on the way out, not a second
// instrument. Turn it off when nothing queries the old names any more.
const (
	currentPrefix = "kube_res_foresight_"
	legacyPrefix  = "kube_resource_fcst_"
)

// LegacyNames is whether /metrics also carries the series under the earlier name (the system's metrics.legacy_names setting).
var LegacyNames bool

// withLegacyNames returns a gatherer that adds the earlier names to what the inner one gathers.
func withLegacyNames(inner prometheus.Gatherer) prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		families, err := inner.Gather()
		out := append([]*dto.MetricFamily(nil), families...)
		for _, family := range families {
			if rest, ok := strings.CutPrefix(family.GetName(), currentPrefix); ok {
				copied := proto.Clone(family).(*dto.MetricFamily)
				copied.Name = proto.String(legacyPrefix + rest)
				out = append(out, copied)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
		return out, err
	})
}
