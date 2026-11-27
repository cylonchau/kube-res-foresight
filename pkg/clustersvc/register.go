package clustersvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The registration of workloads. The operator reports the workloads of its cluster that carry the label clusteragent.LabelRegister=true; what the platform does with them is decided
// here, by the switch of the cluster. A workload that is not a module yet becomes one that is SWITCHED OFF, in the observe mode, with nobody's parameters in it but what the operator
// read of the workload: nothing is scaled, collected or forecast for it until a person has given the maximum of its replicas and its metric sources and switched it on
// (model.ForecastModule.MissingToEnable). A workload that already is a module is left exactly as it is, however often it is reported.

// maxModuleName is the width of the column of a module's name.
const maxModuleName = 100

// moduleNameFor is the name a registered workload gets: namespace-workload, which is what people call it; when that is taken (another cluster has the same workload, or a module
// was named so) the kind and then the cluster are added, and at last a number.
func moduleNameFor(c clusteragent.Candidate, cluster string, attempt int) string {
	base := c.Namespace + "-" + c.Name
	switch {
	case attempt == 1:
		base += "-" + strings.ToLower(c.Kind)
	case attempt == 2 && cluster != "":
		base += "-" + cluster
	case attempt >= 2:
		base = fmt.Sprintf("%s-%d", base, attempt)
	}
	if len(base) > maxModuleName {
		base = base[:maxModuleName]
	}
	return base
}

// Register makes modules of the candidates the operator of the cluster reported. With the switch of the cluster off it does nothing and says so. A module is never made twice for
// one workload (the workload is unique in its cluster), and a registered one is never changed.
func (s *Service) Register(ctx context.Context, cl *model.Cluster, b clusteragent.RegistrationBatch) (*clusteragent.RegistrationResult, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	res := &clusteragent.RegistrationResult{Enabled: cl.AutoRegister, Registered: []string{}, Skipped: []clusteragent.Skip{}}
	if !cl.AutoRegister || s.Modules == nil {
		return res, nil
	}
	// a module of the default cluster names no cluster
	cluster := cl.Name
	if cl.IsDefault {
		cluster = ""
	}
	for _, w := range b.Workloads {
		who := w.Namespace + "/" + w.Name
		// a module that registered itself and was deleted by a person stays deleted until a person allows it again
		if gone, err := s.Modules.Tombstoned(ctx, cluster, strings.TrimSpace(w.Namespace), w.Kind, strings.TrimSpace(w.Name)); err == nil && gone {
			res.Declined++
			continue
		}
		made, err := s.registerOne(ctx, cluster, w)
		switch {
		case err == nil:
			res.Registered = append(res.Registered, made)
		case errors.Is(err, rdb.ErrWorkloadTaken):
			res.Existing++
		default:
			res.Skipped = append(res.Skipped, clusteragent.Skip{Workload: who, Reason: err.Error()})
		}
	}
	return res, nil
}

func (s *Service) registerOne(ctx context.Context, cluster string, w clusteragent.Candidate) (string, error) {
	m := &model.ForecastModule{
		Cluster: cluster, Enabled: false, Mode: model.ModeObserve, AutoRegistered: true,
		Namespace: strings.TrimSpace(w.Namespace), WorkloadKind: w.Kind, Workload: strings.TrimSpace(w.Name), Container: w.Container,
		Description: "Registered by itself from the label " + clusteragent.LabelRegister + ": complete its parameters, then switch it on.",
	}
	if w.Requests != nil {
		// what the Pod template asks for: the figures the replicas are worked out against, read from the workload and not asked of anybody
		m.PodCPURequestMilli, m.PodMemoryRequestBytes = w.Requests.CPUMilli, w.Requests.MemoryBytes
	}
	for attempt := 0; attempt < 12; attempt++ {
		m.Name = moduleNameFor(w, cluster, attempt)
		if err := m.Validate(); err != nil {
			return "", err
		}
		err := s.Modules.Create(ctx, m)
		if errors.Is(err, rdb.ErrNameTaken) {
			continue
		}
		if err != nil {
			return "", err
		}
		s.takeParameters(ctx, m, w)
		return m.Name, nil
	}
	return "", errors.New("no free module name was found for it")
}

// takeParameters copies the parameters of the module named by the annotation copy-from onto the new one (all the groups). The new module stays switched off and registered; a
// module that cannot be found, or that cannot be copied, is no reason to lose the registration, so it is left as it was made.
func (s *Service) takeParameters(ctx context.Context, m *model.ForecastModule, w clusteragent.Candidate) {
	name := strings.TrimSpace(w.CopyFrom)
	if name == "" || s.Copier == nil {
		return
	}
	src, err := s.Modules.ByName(ctx, name)
	if err != nil || src == nil {
		return
	}
	_, _ = s.Copier.Copy(ctx, src.ID, m.ID, nil)
}
