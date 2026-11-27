package controller

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The one thing the operator changes in a cluster is the replicas of the workloads of the platform's modules, through their scale subresource. Everything else it reads.
// The two other writes are the ones every controller that is elected makes: the lease it holds, and the events it records about the election. This test reads the
// permissions where they are written (the markers, the generated role, the role of the Helm chart) and holds that line.

type rbacRule struct {
	Groups    []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}

type clusterRoleDoc struct {
	Rules []rbacRule `yaml:"rules"`
}

var writeVerbs = map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "deletecollection": true}

// writesOf lists "group/resource" for every resource a rule lets the operator change, with the write verbs.
func writesOf(rules []rbacRule) map[string][]string {
	out := map[string][]string{}
	for _, r := range rules {
		var ws []string
		for _, v := range r.Verbs {
			if writeVerbs[v] || v == "*" {
				ws = append(ws, v)
			}
		}
		if len(ws) == 0 {
			continue
		}
		for _, g := range r.Groups {
			for _, res := range r.Resources {
				key := g + "/" + res
				out[key] = append(out[key], ws...)
			}
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// allowedWrites is every write the operator may have, and why.
var allowedWrites = map[string]string{
	"apps/deployments/scale":  "the replicas of a module's workload (the only business write)",
	"apps/statefulsets/scale": "the replicas of a module's workload (the only business write)",
	// only when the operator is started to adjust the Pod spec (--adjust-pod-spec): the Helm chart gives it with adjustPodSpec.enabled and deploy/operator/rbac/role-pod-spec.yaml is the role to add
	"apps/deployments":           "the Pod template of a module's workload in auto mode (Requests, Limits, Node Selector), opt-in",
	"apps/statefulsets":          "the Pod template of a module's workload in auto mode (Requests, Limits, Node Selector), opt-in",
	"coordination.k8s.io/leases": "the leader election lease",
	"/events":                    "the events the election records about itself (the Helm role only)",
}

func readerOf(t *testing.T, rules []rbacRule, group, resource string) []string {
	t.Helper()
	for _, r := range rules {
		for _, g := range r.Groups {
			for _, res := range r.Resources {
				if g == group && res == resource {
					return r.Verbs
				}
			}
		}
	}
	return nil
}

func markerRules(t *testing.T) []rbacRule {
	t.Helper()
	raw, err := os.ReadFile("rbac.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^// \+kubebuilder:rbac:groups=([^,]+),resources=([^,]+),verbs=(\S+)$`)
	var rules []rbacRule
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		group := m[1]
		if group == "core" {
			group = ""
		}
		rules = append(rules, rbacRule{Groups: []string{group}, Resources: strings.Split(m[2], ";"), Verbs: strings.Split(m[3], ";")})
	}
	require.NotEmpty(t, rules)
	return rules
}

func generatedRole(t *testing.T) []rbacRule {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "operator", "rbac", "role.yaml"))
	require.NoError(t, err)
	var doc clusterRoleDoc
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	return doc.Rules
}

// helmRole reads the ClusterRole of the chart: the template lines that are Helm directives are dropped (the rules of a switch that is off are in it as well, which is the
// strictest reading: every rule that could be there is held to the line).
func helmRole(t *testing.T) []rbacRule {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "helm", "kube-res-foresight-operator", "templates", "clusterrole.yaml"))
	require.NoError(t, err)
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "{{") {
			continue
		}
		kept = append(kept, line)
	}
	first := strings.Split(strings.Join(kept, "\n"), "\n---")[0]
	var doc clusterRoleDoc
	require.NoError(t, yaml.Unmarshal([]byte(first), &doc))
	require.NotEmpty(t, doc.Rules)
	return doc.Rules
}

func TestTheOperatorWritesNothingButTheScaleOfItsWorkloadsAndItsLease(t *testing.T) {
	for name, rules := range map[string][]rbacRule{"the markers": markerRules(t), "the generated role": generatedRole(t), "the Helm chart role": helmRole(t)} {
		for resource, verbs := range writesOf(rules) {
			_, ok := allowedWrites[resource]
			assert.True(t, ok, "%s let the operator change %s (%v): the operator only reads, but for the scale subresource of its workloads and its election lease", name, resource, verbs)
		}
	}
	// the events of the election are the Helm role's alone: the markers and the generated role do not write them
	assert.Contains(t, writesOf(helmRole(t)), "/events", "the test reads the Helm role (an empty read would hold nothing)")
	assert.NotContains(t, writesOf(markerRules(t)), "/events")
	assert.NotContains(t, writesOf(generatedRole(t)), "/events")
}

// The write on the workloads themselves is opt-in: the markers and the role generated from them (what a plain install uses) have none of it.
func TestTheDefaultRoleHasNoWriteOnTheWorkloadsThemselves(t *testing.T) {
	for name, rules := range map[string][]rbacRule{"the markers": markerRules(t), "the generated role": generatedRole(t)} {
		writes := writesOf(rules)
		assert.NotContains(t, writes, "apps/deployments", name)
		assert.NotContains(t, writes, "apps/statefulsets", name)
	}
}

func TestTheEventsAndTheClusterAreReadOnly(t *testing.T) {
	for name, rules := range map[string][]rbacRule{"the markers": markerRules(t), "the generated role": generatedRole(t)} {
		for _, res := range []string{"events", "nodes", "pods"} {
			verbs := readerOf(t, rules, "", res)
			assert.ElementsMatch(t, []string{"get", "list", "watch"}, verbs, "%s: %s are read, nothing more", name, res)
		}
		for _, res := range []string{"deployments", "statefulsets", "daemonsets"} {
			assert.ElementsMatch(t, []string{"get", "list", "watch"}, readerOf(t, rules, "apps", res), "%s: %s", name, res)
		}
		assert.ElementsMatch(t, []string{"get", "list", "watch"}, readerOf(t, rules, "autoscaling", "horizontalpodautoscalers"), name)
		assert.ElementsMatch(t, []string{"get", "update", "patch"}, readerOf(t, rules, "apps", "deployments/scale"), "%s: the scale subresource is the one write", name)
	}
}

func TestTheMarkersAndTheGeneratedRoleSayTheSame(t *testing.T) {
	flat := func(rules []rbacRule) []string {
		var out []string
		for _, r := range rules {
			for _, g := range r.Groups {
				for _, res := range r.Resources {
					verbs := append([]string{}, r.Verbs...)
					sort.Strings(verbs)
					out = append(out, g+"/"+res+":"+strings.Join(verbs, ","))
				}
			}
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, flat(markerRules(t)), flat(generatedRole(t)), "deploy/operator/rbac/role.yaml is what the markers in rbac.go generate (run controller-gen after changing them)")
}

// Only the leader acts on the cluster (E6): what scales a workload and what reads the cluster for the platform runs on the elected replica, like every controller that
// takes a lease; a replica that stands by holds no informer and sends nothing. The two that run on every replica do not touch the cluster: one sends what the leader queued,
// the other asks the platform for its settings.
func TestOnlyTheElectedReplicaActsOnOrReadsTheCluster(t *testing.T) {
	for name, r := range map[string]interface{ NeedLeaderElection() bool }{
		"the module sync (it scales)": &ModuleSync{},
		"the inventory reader":        &InventoryRunner{},
		"the event collector":         &EventCollector{},
		"the change collector":        &ChangeCollector{},
	} {
		assert.True(t, r.NeedLeaderElection(), name)
	}
	for name, r := range map[string]interface{ NeedLeaderElection() bool }{
		"the outbox (it sends what the leader queued)": &Outbox{},
		"the settings refresh (it asks the platform)":  &SettingsRefresher{},
	} {
		assert.False(t, r.NeedLeaderElection(), name)
	}
}
