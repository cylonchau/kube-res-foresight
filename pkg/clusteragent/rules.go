package clusteragent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The origin of an event: who did what it says.
const (
	// OriginAuto is an automatic component (an autoscaler, a controller, the kubelet, the scheduler, a cloud controller); Category says which kind.
	OriginAuto = "auto"
	// OriginManual is a person, and is only said when a rule of the platform says it (an event does not tell by itself).
	OriginManual = "manual"
	// OriginUnclassified is an event no rule knows: it is kept as it came, and nothing is guessed.
	OriginUnclassified = "unclassified"
)

// Rules read an event: who said it. The cause of a change in a cluster is in the events (a node is removed, a Pod is evicted, a workload is scaled), said by the component
// that did it, so the component, and sometimes the reason, tells whether it was an automatic component (an autoscaler, a controller, the kubelet, the scheduler, a cloud
// controller) or a person, and which kind. An event is never changed or dropped for the answer: the rules only add Origin, Category and the name of the rule.
//
// Supporting another cloud or another autoscaler is a rule, not code: the built-in rules are the components Kubernetes itself has, and the platform administrator adds
// rules in the system parameter cluster.event_rules (a JSON list, see Rule), which are tried first. An event that no rule knows is "unclassified": it is shown as it
// came, and nothing is guessed. A person is only said when a rule says it: an event does not tell by itself that a person did it, so no built-in rule says it.

// Rule matches events by component, reason and kind of object. An empty field matches anything; a pattern is a name, or a name with a leading and/or trailing "*"
// (suffix, prefix, contains), compared without regard to case.
type Rule struct {
	Name      string `json:"name,omitempty"`
	Component string `json:"component,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Kind      string `json:"kind,omitempty"`
	// Origin is auto or manual; Category says what kind of component it is (autoscaler, controller, kubelet, scheduler, cloud, ...).
	Origin   string `json:"origin"`
	Category string `json:"category,omitempty"`
}

// Subject is what a rule looks at.
type Subject struct {
	Component string
	Reason    string
	Kind      string
}

// Result is what the rules make of an event.
type Result struct {
	Origin   string
	Category string
	Rule     string
}

func pattern(p, s string) bool {
	p, s = strings.ToLower(strings.TrimSpace(p)), strings.ToLower(s)
	if p == "" {
		return true
	}
	lead, trail := strings.HasPrefix(p, "*"), strings.HasSuffix(p, "*")
	core := strings.Trim(p, "*")
	switch {
	case lead && trail:
		return strings.Contains(s, core)
	case lead:
		return strings.HasSuffix(s, core)
	case trail:
		return strings.HasPrefix(s, core)
	}
	return s == core
}

func (r Rule) matches(s Subject) bool {
	return pattern(r.Component, s.Component) && pattern(r.Reason, s.Reason) && pattern(r.Kind, s.Kind)
}

// Builtin is the rules of the components Kubernetes and the common autoscalers are known by. The names of the components are the ones their events carry as the source
// (the reporting controller). The Karpenter, KEDA and cloud-controller entries match by a part of the name and have not been checked against an event of a real cluster:
// if they do not match yours, the events are "unclassified" and a rule in cluster.event_rules fixes it.
func Builtin() []Rule {
	return []Rule{
		{Name: "cluster-autoscaler", Component: "cluster-autoscaler*", Origin: OriginAuto, Category: "autoscaler"},
		{Name: "horizontal-pod-autoscaler", Component: "horizontal-pod-autoscaler", Origin: OriginAuto, Category: "autoscaler"},
		{Name: "karpenter", Component: "*karpenter*", Origin: OriginAuto, Category: "autoscaler"},
		{Name: "keda", Component: "keda*", Origin: OriginAuto, Category: "autoscaler"},
		{Name: "cloud-controller", Component: "*cloud-controller*", Origin: OriginAuto, Category: "cloud"},
		{Name: "kubelet", Component: "kubelet", Origin: OriginAuto, Category: "kubelet"},
		{Name: "scheduler", Component: "*scheduler*", Origin: OriginAuto, Category: "scheduler"},
		{Name: "controller-manager", Component: "*controller-manager*", Origin: OriginAuto, Category: "controller"},
		{Name: "controller", Component: "*-controller", Origin: OriginAuto, Category: "controller"},
	}
}

// ParseRules reads the rules of the system parameter: a JSON list. An empty text is no rules; a bad one is an error (and the platform then uses the built-in rules only).
func ParseRules(text string) ([]Rule, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var rules []Rule
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		return nil, fmt.Errorf("%w: the event rules are not a JSON list of rules: %v", ErrInvalid, err)
	}
	for i := range rules {
		r := &rules[i]
		r.Origin = strings.ToLower(strings.TrimSpace(r.Origin))
		if r.Origin != OriginAuto && r.Origin != OriginManual {
			return nil, fmt.Errorf("%w: rule %d: origin must be %q or %q", ErrInvalid, i+1, OriginAuto, OriginManual)
		}
		if strings.TrimSpace(r.Component)+strings.TrimSpace(r.Reason)+strings.TrimSpace(r.Kind) == "" {
			return nil, fmt.Errorf("%w: rule %d matches every event: give it a component, a reason or a kind", ErrInvalid, i+1)
		}
		if r.Name == "" {
			r.Name = fmt.Sprintf("custom-%d", i+1)
		}
	}
	return rules, nil
}

// Classify reads one event: the custom rules first, in order, then the built-in ones; the first that matches says. No match is "unclassified".
func Classify(custom []Rule, s Subject) Result {
	for _, set := range [][]Rule{custom, Builtin()} {
		for _, r := range set {
			if r.matches(s) {
				return Result{Origin: r.Origin, Category: r.Category, Rule: r.Name}
			}
		}
	}
	return Result{Origin: OriginUnclassified}
}
