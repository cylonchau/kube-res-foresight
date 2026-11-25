// Package clusteragent is what the operator and the platform share of the Cluster Agent (P12): the namespaces an operator is asked to collect from, the Kubernetes Events
// it reports as they were said, and the rules the platform reads an event with (who said it: an autoscaler, a controller, the kubelet, a cloud, a person). It holds no
// database and no Kubernetes client. The operator only reads: nothing here changes anything in a cluster.
package clusteragent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Limits of what is accepted from an operator: a batch that is bigger is refused whole, so the operator finds out.
const (
	MaxBatchEvents  = 500
	MaxScopeEntries = 200
	maxNameLen      = 253
)

// HeaderCluster is the header an operator names its cluster in (--cluster), on every request to the platform; an operator started without one sends none and belongs to
// the default cluster.
const HeaderCluster = "X-kube-res-foresight-Cluster"

// ErrInvalid is a batch or a list that does not pass its own checks.
var ErrInvalid = errors.New("invalid")

// Scope is the namespaces the operator collects the namespaced objects of (the events, today). A namespace is collected when the include list is empty or names it,
// and the exclude list does not. An entry is a namespace name, or a prefix with a trailing "*". An object that is in no namespace (a Node, and the events about
// one) is always collected: a node is not a namespace to leave out. The replicas of a module are not limited by it.
type Scope struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

func matches(entry, name string) bool {
	if strings.HasSuffix(entry, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(entry, "*"))
	}
	return entry == name
}

// Allows says whether the objects of the namespace are collected.
func (s Scope) Allows(namespace string) bool {
	if namespace == "" {
		return true
	}
	for _, e := range s.Exclude {
		if matches(e, namespace) {
			return false
		}
	}
	if len(s.Include) == 0 {
		return true
	}
	for _, e := range s.Include {
		if matches(e, namespace) {
			return true
		}
	}
	return false
}

// Empty is a scope that limits nothing.
func (s Scope) Empty() bool { return len(s.Include) == 0 && len(s.Exclude) == 0 }

// CleanNames checks and tidies a list of namespace entries: names are lower-case DNS labels, optionally with a trailing "*"; blanks and repeats are dropped.
func CleanNames(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" || seen[e] {
			continue
		}
		body := strings.TrimSuffix(e, "*")
		if strings.Contains(body, "*") || len(e) > 63 || body != strings.Trim(body, ".") {
			return nil, fmt.Errorf("%w: %q is not a namespace name (a name, or a prefix ending in *)", ErrInvalid, raw)
		}
		for _, r := range body {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return nil, fmt.Errorf("%w: %q is not a namespace name (lower-case letters, digits and - only, and a trailing * for a prefix)", ErrInvalid, raw)
			}
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) > MaxScopeEntries {
		return nil, fmt.Errorf("%w: at most %d namespace entries", ErrInvalid, MaxScopeEntries)
	}
	sort.Strings(out)
	return out, nil
}

// ParseNames reads the JSON list the platform stores; an unreadable value is an empty list.
func ParseNames(stored string) []string {
	var out []string
	if strings.TrimSpace(stored) == "" || json.Unmarshal([]byte(stored), &out) != nil || out == nil {
		return []string{}
	}
	return out
}

// FormatNames writes a list the way the platform stores it.
func FormatNames(list []string) string {
	if len(list) == 0 {
		return ""
	}
	raw, _ := json.Marshal(list)
	return string(raw)
}

// Settings is what the platform tells an operator about the cluster it was started for (GET /operator/cluster-settings): which namespaces to collect from, whether to
// collect events, and the reasons of events that are not worth sending. The operator keeps the last answer; a change on the platform reaches a running operator.
type Settings struct {
	Cluster       string `json:"cluster"`
	Namespaces    Scope  `json:"namespaces"`
	CollectEvents bool   `json:"collect_events"`
	// CollectChanges says whether the operator reports the state changes of the nodes, Pods, workloads and HPAs it watches (see changes.go).
	CollectChanges bool     `json:"collect_changes"`
	SkipReasons    []string `json:"skip_reasons"`
}

// Event is a Kubernetes Event as the operator reports it: what was said, by whom, about what, and when. Nothing is translated or merged. It is both the wire format and
// what the platform keeps as the raw record.
type Event struct {
	UID       string    `json:"uid"`
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Component string    `json:"component"`
	Host      string    `json:"host,omitempty"`
	Count     int       `json:"count"`
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace,omitempty"`
	Name      string    `json:"name"`
	ObjectUID string    `json:"object_uid,omitempty"`
	FirstAt   time.Time `json:"first_at"`
	LastAt    time.Time `json:"last_at"`
}

// Batch is one report of events: from one operator, for the cluster its credential is for.
type Batch struct {
	Operator string  `json:"operator,omitempty"`
	Events   []Event `json:"events"`
}

// Validate refuses a batch that is too big or an event that cannot be told apart from another (no uid).
func (b Batch) Validate() error {
	if len(b.Events) > MaxBatchEvents {
		return fmt.Errorf("%w: %d events in a batch (at most %d)", ErrInvalid, len(b.Events), MaxBatchEvents)
	}
	for i, e := range b.Events {
		if strings.TrimSpace(e.UID) == "" {
			return fmt.Errorf("%w: event %d has no uid", ErrInvalid, i)
		}
		if e.LastAt.IsZero() && e.FirstAt.IsZero() {
			return fmt.Errorf("%w: event %s has no time", ErrInvalid, e.UID)
		}
	}
	return nil
}

// Clip cuts a text to n bytes without splitting a character.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}

// Skipped says whether an event with this reason is one the operator is told not to send (noise such as a container being pulled or started).
func Skipped(reasons []string, reason string) bool {
	for _, r := range reasons {
		if strings.EqualFold(strings.TrimSpace(r), reason) {
			return true
		}
	}
	return false
}
