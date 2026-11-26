package clusteragent

import (
	"fmt"
	"strings"
)

// The registration of workloads (auto-registration): a workload that carries the label LabelRegister=true is a candidate to become a module of the platform. The operator only
// reads the cluster and reports the candidates; whether one is registered is the platform's decision (the switch of the cluster), and a module that is registered this way is
// made switched off, in the observe mode, so nothing is scaled until someone has completed its parameters and turned it on.
//
// Label or annotation: the switch is a label, because a label can be selected on by the API server (the operator lists only the workloads that carry it, in every namespace,
// without reading the others) and its value is a fixed word, while an annotation cannot be selected on and is for what only people and tools read. What goes with the switch
// (the module to take the parameters from) is an annotation: it is free text, and nothing selects on it.
const (
	// LabelRegister is the switch on the workload: the value "true" makes it a candidate; any other value (or no label) leaves it alone.
	LabelRegister = "kube-res-foresight.io/register"
	// AnnotationCopyFrom names a module whose parameters a newly registered module takes over (the groups that are not specific to a workload).
	AnnotationCopyFrom = "kube-res-foresight.io/copy-from"
)

// Limits of a report of candidates.
const MaxCandidates = 500

// Candidate is one workload that asked to be registered, with what the operator could read of it: the facts the platform fills a new module with and asks nobody about.
type Candidate struct {
	Namespace string `json:"namespace"`
	// Kind is Deployment, StatefulSet or DaemonSet.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Container is the name of the container when the Pod template has exactly one (the {{container}} of the metric templates); empty when it has several.
	Container string `json:"container,omitempty"`
	// Replicas is what the workload is set to now (nil for a DaemonSet). Requests is the sum over the containers of the Pod template (0 = not set). They are shown to the person
	// who completes the module; they are not turned into bounds.
	Replicas *int32 `json:"replicas,omitempty"`
	Requests *Res   `json:"requests,omitempty"`
	// CopyFrom is the value of the annotation AnnotationCopyFrom.
	CopyFrom string `json:"copy_from,omitempty"`
}

// RegistrationBatch is one report of candidates.
type RegistrationBatch struct {
	Operator  string      `json:"operator,omitempty"`
	Workloads []Candidate `json:"workloads"`
}

// Validate refuses a batch that is too big, or a candidate that cannot be told apart.
func (b RegistrationBatch) Validate() error {
	if len(b.Workloads) > MaxCandidates {
		return fmt.Errorf("%w: %d workloads in a report (at most %d)", ErrInvalid, len(b.Workloads), MaxCandidates)
	}
	for i, w := range b.Workloads {
		switch w.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
		default:
			return fmt.Errorf("%w: workload %d has kind %q", ErrInvalid, i, w.Kind)
		}
		if strings.TrimSpace(w.Namespace) == "" || strings.TrimSpace(w.Name) == "" {
			return fmt.Errorf("%w: workload %d needs a namespace and a name", ErrInvalid, i)
		}
	}
	return nil
}

// Skip is a candidate that was not registered, and why.
type Skip struct {
	Workload string `json:"workload"`
	Reason   string `json:"reason"`
}

// RegistrationResult is what the platform did with a report. Enabled is false when the switch of the cluster is off (nothing was registered).
type RegistrationResult struct {
	Enabled bool `json:"enabled"`
	// Registered are the names of the modules that were made; Existing how many candidates already were modules.
	Registered []string `json:"registered"`
	Existing   int      `json:"existing"`
	Skipped    []Skip   `json:"skipped"`
}
