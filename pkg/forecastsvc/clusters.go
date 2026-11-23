package forecastsvc

import (
	"context"
	"sort"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The clusters the platform manages, for the cluster dashboard: a cluster is a Prometheus data source that has cluster metric templates declared on it (the metric
// type "cluster" of the metric templates), plus the data source the system parameter cluster.datasource_id names. What a cluster shows is what its templates
// declare; the list says how much of the set each declares.

// ClusterEntry is one managed cluster.
type ClusterEntry struct {
	DatasourceID uint   `json:"datasource_id"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	// Declared is how many of the cluster metric kinds have an enabled template on the data source; Total is how many there are.
	Declared int `json:"declared"`
	Total    int `json:"total"`
	// Missing are the kinds not declared.
	Missing []string `json:"missing"`
	// Primary is true for the data source the system parameter cluster.datasource_id names.
	Primary bool `json:"primary"`
}

// ClusterList is the clusters and their number.
type ClusterList struct {
	Count    int             `json:"count"`
	Clusters []*ClusterEntry `json:"clusters"`
}

// ClusterList lists the managed clusters, the primary one first and then by name.
func (s *Service) ClusterList(ctx context.Context) (*ClusterList, error) {
	kinds := make([]string, 0, len(model.ScopedKinds))
	for kind := range model.ScopedKinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	declared := map[uint]map[string]bool{}
	if s.Mappings != nil {
		list, err := s.Mappings.Scoped(ctx, 0)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			if declared[m.DatasourceID] == nil {
				declared[m.DatasourceID] = map[string]bool{}
			}
			declared[m.DatasourceID][m.Metric] = true
		}
	}
	primary := uint(s.Count(ctx, timing.ClusterDatasource))
	if primary != 0 && declared[primary] == nil {
		declared[primary] = map[string]bool{}
	}

	out := &ClusterList{Clusters: []*ClusterEntry{}}
	for id, have := range declared {
		ds, err := s.Datasources.GetByID(ctx, id)
		if err != nil {
			continue // a data source that is gone is no cluster
		}
		entry := &ClusterEntry{DatasourceID: id, Name: ds.Name, Enabled: ds.Enabled, Total: len(kinds), Missing: []string{}, Primary: id == primary}
		for _, kind := range kinds {
			if have[kind] {
				entry.Declared++
			} else {
				entry.Missing = append(entry.Missing, kind)
			}
		}
		out.Clusters = append(out.Clusters, entry)
	}
	sort.Slice(out.Clusters, func(i, j int) bool {
		a, b := out.Clusters[i], out.Clusters[j]
		if a.Primary != b.Primary {
			return a.Primary
		}
		return a.Name < b.Name
	})
	out.Count = len(out.Clusters)
	return out, nil
}
