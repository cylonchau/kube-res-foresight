package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/clustersvc"
	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
)

// ClusterUsage asks Prometheus for the usage of an object of a cluster at a time (P12 E7): what a node, a Pod or a workload used when it changed, a little before and a
// little after. It is the Service of the platform's own queries (the node figures of the cluster page, the Pod and workload figures of the dashboard), asked at the time of
// the change and not at the present, so the figures are the ones the pages show. It never answers a figure it did not get: no series is a note, never a zero.
type ClusterUsage struct{ Service *Service }

// the figures asked, by the kind of object: the names of the shared queries.
var usageQueries = map[string][2]string{
	clusteragent.KindNode:     {"node_cpu_usage", "node_memory_usage"},
	clusteragent.KindPod:      {"pod_cpu_usage", "pod_memory_usage"},
	clusteragent.KindWorkload: {"workload_cpu_usage", "workload_memory_usage"},
}

// UsageAt implements clustersvc.UsageSource.
func (u ClusterUsage) UsageAt(ctx context.Context, datasourceID uint, sub clustersvc.UsageSubject, at time.Time) (clustersvc.UsageValue, string, error) {
	s := u.Service
	names, ok := usageQueries[sub.Kind]
	if !ok {
		return clustersvc.UsageValue{}, "", fmt.Errorf("%w: no usage is asked of a %s", clustersvc.ErrNoUsageSource, sub.Kind)
	}
	if datasourceID == 0 {
		id, err := s.ClusterDatasourceID(ctx, 0)
		if err != nil {
			if errors.Is(err, ErrCostNoDatasource) {
				return clustersvc.UsageValue{}, "", fmt.Errorf("%w: %s", clustersvc.ErrNoUsageSource, msgNoDatasource)
			}
			return clustersvc.UsageValue{}, "", err
		}
		datasourceID = id
	}
	ds, err := s.clusterQuerier(ctx, datasourceID)
	if err != nil {
		return clustersvc.UsageValue{}, "", fmt.Errorf("%w: %v", clustersvc.ErrNoUsageSource, err)
	}
	q, err := s.vectorQuerierOf(ctx, ds)
	if err != nil {
		return clustersvc.UsageValue{}, ds.Name, err
	}
	set, origin, _ := s.QueriesFor(ctx, ds.ID)
	values := map[string]string{}
	label := "node"
	switch sub.Kind {
	case clusteragent.KindPod:
		values["namespace"], values["workload"], label = sub.Namespace, sub.Workload, "pod"
	case clusteragent.KindWorkload:
		values["namespace"], values["workload"], label = sub.Namespace, sub.Workload, ""
	}
	out := clustersvc.UsageValue{}
	var notes []string
	ask := func(name string) *float64 {
		if sub.Kind == clusteragent.KindNode && origin[name].source != QueryFromMapping {
			notes = append(notes, fmt.Sprintf(msgNotDeclared, resourcequery.MappingKind[name]))
			return nil
		}
		samples, err := q.QueryVector(ctx, resourcequery.Expand(set[name], values), at)
		if err != nil {
			notes = append(notes, name+": "+err.Error())
			return nil
		}
		for _, sm := range samples {
			if label != "" && sm.Labels[label] != sub.Name {
				continue
			}
			if math.IsNaN(sm.Value) || math.IsInf(sm.Value, 0) {
				continue
			}
			v := sm.Value
			return &v
		}
		notes = append(notes, name+": the data source has no series for it")
		return nil
	}
	out.CPU, out.Memory = ask(names[0]), ask(names[1])
	if out.CPU == nil && out.Memory == nil {
		// a data source that could not be reached at all is asked again; one that has no series, or no template, is not
		if len(notes) > 0 && queryFailed(notes) {
			return out, ds.Name, errors.New(notes[0])
		}
		out.Note = joinNotes(notes)
	} else if len(notes) > 0 {
		out.Note = joinNotes(notes)
	}
	return out, ds.Name, nil
}

// queryFailed says whether the notes are of a query that failed (as against one that had no series or no template).
func queryFailed(notes []string) bool {
	for _, n := range notes {
		if !strings.Contains(n, "has no series") && !strings.Contains(n, "not declared") {
			return true
		}
	}
	return false
}

func joinNotes(notes []string) string {
	out := ""
	for i, n := range notes {
		if i > 0 {
			out += "; "
		}
		out += n
	}
	return clusteragent.Clip(out, 250)
}
