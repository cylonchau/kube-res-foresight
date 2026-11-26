package clustersvc

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
)

// What the one-click AI diagnosis of a change is given: the facts the platform holds about it, and nothing else. They are the change (what it went from and to, the reasons the
// object itself gave, the containers and what the object asks for), the usage around it, the events about the object around that time, and the changes tied to it with the
// evidence of the tie. The model reads them as data; it is not told the name of the cluster or of the access key.

// maxFactsBytes keeps the facts short enough for any model: a node holds the list of its Pods, and the events of a busy object are many.
const maxFactsBytes = 24 << 10

const (
	maxFactEvents = 12
	maxFactLinks  = 6
)

type factEvent struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Component string `json:"component,omitempty"`
	Count     int    `json:"count"`
	LastAt    string `json:"last_at"`
}

type factContainer struct {
	Name         string `json:"name"`
	State        string `json:"state,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Message      string `json:"message,omitempty"`
	ExitCode     int32  `json:"exit_code,omitempty"`
	Restarts     int32  `json:"restarts"`
	LastReason   string `json:"last_reason,omitempty"`
	LastExitCode int32  `json:"last_exit_code,omitempty"`
}

type factLink struct {
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
	Evidence  string `json:"evidence"`
	Object    string `json:"object"`
	Type      string `json:"type"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	At        string `json:"at"`
	Reason    string `json:"reason,omitempty"`
}

type factUsage struct {
	At          string   `json:"at"`
	Label       string   `json:"label"`
	CPUCores    *float64 `json:"cpu_cores"`
	MemoryBytes *float64 `json:"memory_bytes"`
}

type facts struct {
	Object     string                `json:"object"`
	Kind       string                `json:"kind"`
	ChangeType string                `json:"change_type"`
	From       string                `json:"from,omitempty"`
	To         string                `json:"to,omitempty"`
	At         string                `json:"at"`
	Node       string                `json:"node,omitempty"`
	Workload   string                `json:"workload,omitempty"`
	Reasons    []clusteragent.Reason `json:"reasons,omitempty"`
	Containers []factContainer       `json:"containers,omitempty"`
	Requests   *clusteragent.Res     `json:"requests,omitempty"`
	Limits     *clusteragent.Res     `json:"limits,omitempty"`
	Replicas   map[string]int32      `json:"replicas,omitempty"`
	Taints     []string              `json:"taints,omitempty"`
	Usage      []factUsage           `json:"usage,omitempty"`
	UsageNote  string                `json:"usage_note,omitempty"`
	Events     []factEvent           `json:"events"`
	Causes     []factLink            `json:"possible_causes"`
	Effects    []factLink            `json:"possible_effects"`
}

func objectOf(c ChangeDTO) string {
	if c.Namespace != "" {
		return c.Namespace + "/" + c.Name
	}
	return c.Name
}

func atOf(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func linkFact(l Link) factLink {
	out := factLink{Relation: l.Relation, Direction: l.Direction, Evidence: clusteragent.Clip(l.Evidence, 300), Object: l.Change.Kind + " " + objectOf(l.Change),
		Type: l.Change.Type, From: l.Change.From, To: l.Change.To, At: atOf(l.Change.OccurredAt)}
	if len(l.Change.Reasons) > 0 {
		r := l.Change.Reasons[0]
		out.Reason = clusteragent.Clip(strings.TrimSpace(r.Reason+" "+r.Message), 300)
	}
	return out
}

// DiagnoseFacts is the facts about one explained change as the text the model is given (JSON, UTC times, at most maxFactsBytes). A figure is the platform's: usage is what
// Prometheus gave, and a missing one is null, not zero.
func DiagnoseFacts(e *Explanation) string {
	c := e.Change
	f := facts{Object: objectOf(c), Kind: c.Kind, ChangeType: c.Type, From: c.From, To: c.To, At: atOf(c.OccurredAt), Node: c.Node, Workload: c.Workload,
		Events: []factEvent{}, Causes: []factLink{}, Effects: []factLink{}}
	for _, r := range c.Reasons {
		r.Message = clusteragent.Clip(r.Message, 400)
		f.Reasons = append(f.Reasons, r)
	}
	if s := c.Snapshot; s != nil {
		for _, ct := range s.Containers {
			f.Containers = append(f.Containers, factContainer{Name: ct.Name, State: ct.State, Reason: ct.Reason, Message: clusteragent.Clip(ct.Message, 400), ExitCode: ct.ExitCode,
				Restarts: ct.Restarts, LastReason: ct.LastReason, LastExitCode: ct.LastExitCode})
		}
		f.Requests, f.Limits, f.Taints = s.Requests, s.Limits, s.Taints
		replicas := map[string]int32{}
		for k, v := range map[string]*int32{"desired": s.Desired, "ready": s.Ready, "available": s.Available, "min": s.Min, "max": s.Max, "current": s.Current} {
			if v != nil {
				replicas[k] = *v
			}
		}
		if len(replicas) > 0 {
			f.Replicas = replicas
		}
	}
	if u := c.Usage; u != nil {
		f.UsageNote = u.Note
		for _, p := range u.Points {
			f.Usage = append(f.Usage, factUsage{At: atOf(p.At), Label: p.Label, CPUCores: p.CPU, MemoryBytes: p.Memory})
		}
	}
	for i, ev := range e.Events {
		if i >= maxFactEvents {
			break
		}
		f.Events = append(f.Events, factEvent{Type: ev.Type, Reason: ev.Reason, Message: clusteragent.Clip(ev.Message, 500), Component: ev.Component, Count: ev.Count, LastAt: atOf(ev.LastAt)})
	}
	for i, l := range e.Causes {
		if i >= maxFactLinks {
			break
		}
		f.Causes = append(f.Causes, linkFact(l))
	}
	for i, l := range e.Effects {
		if i >= maxFactLinks {
			break
		}
		f.Effects = append(f.Effects, linkFact(l))
	}
	raw, _ := json.MarshalIndent(f, "", " ")
	if len(raw) > maxFactsBytes {
		// the facts that were cut are said to be, so the model does not take what is missing for what did not happen
		return string(raw[:maxFactsBytes]) + "\n… [the facts were cut at " + "24 KiB]"
	}
	return string(raw)
}
