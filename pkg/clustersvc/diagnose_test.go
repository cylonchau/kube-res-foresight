package clustersvc

import (
	"strings"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func TestDiagnoseFactsCarryWhatWasSeen(t *testing.T) {
	at := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	e := &Explanation{
		Change: ChangeDTO{ClusterChange: model.ClusterChange{Kind: "Pod", Namespace: "shop", Name: "web-1", Type: "waiting", From: "Running", To: "ErrImagePull", OccurredAt: at, Cluster: "secret-cluster-name", Operator: "secret-operator"},
			Reasons:  []clusteragent.Reason{{Source: "container", Reason: "ErrImagePull", Message: "pull access denied for web:9"}},
			Snapshot: &clusteragent.Snapshot{Containers: []clusteragent.ContainerState{{Name: "web", State: "waiting", Reason: "ErrImagePull", Restarts: 0}}}},
		Events: []model.ClusterEvent{{Type: "Warning", Reason: "Failed", Message: "Failed to pull image", Count: 3, LastAt: at}},
		Causes: []Link{}, Effects: []Link{},
	}
	got := DiagnoseFacts(e)
	for _, want := range []string{"shop/web-1", "ErrImagePull", "Running", "pull access denied", "Failed to pull image", "2026-11-26T03:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("the facts lack %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"secret-cluster-name", "secret-operator"} {
		if strings.Contains(got, not) {
			t.Errorf("the facts name %q, which the model is not told", not)
		}
	}
}

func TestDiagnoseFactsAreBounded(t *testing.T) {
	big := strings.Repeat("x", 600)
	e := &Explanation{Change: ChangeDTO{ClusterChange: model.ClusterChange{Kind: "Node", Name: "n1", Type: "ready", OccurredAt: time.Now()}}, Causes: []Link{}, Effects: []Link{}}
	for i := 0; i < 200; i++ {
		e.Events = append(e.Events, model.ClusterEvent{Message: big, LastAt: time.Now()})
	}
	if got := DiagnoseFacts(e); len(got) > maxFactsBytes+100 {
		t.Fatalf("the facts are %d bytes", len(got))
	}
}
