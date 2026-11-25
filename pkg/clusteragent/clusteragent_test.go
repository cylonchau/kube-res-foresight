package clusteragent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAScopeCollectsTheIncludedNamespacesMinusTheExcludedOnes(t *testing.T) {
	cases := []struct {
		name  string
		scope Scope
		ns    string
		want  bool
	}{
		{"nothing set collects everything", Scope{}, "shop", true},
		{"an object in no namespace (a node) is always collected", Scope{Include: []string{"shop"}, Exclude: []string{"*"}}, "", true},
		{"the include list limits", Scope{Include: []string{"shop"}}, "billing", false},
		{"the include list admits", Scope{Include: []string{"shop"}}, "shop", true},
		{"the exclude list drops", Scope{Exclude: []string{"kube-system"}}, "kube-system", false},
		{"the exclude list leaves the others", Scope{Exclude: []string{"kube-system"}}, "shop", true},
		{"exclude wins over include", Scope{Include: []string{"shop*"}, Exclude: []string{"shop-test"}}, "shop-test", false},
		{"a trailing star is a prefix", Scope{Include: []string{"shop*"}}, "shop-api", true},
		{"a prefix does not match another name", Scope{Include: []string{"shop*"}}, "billing", false},
		{"a name without a star is the whole name", Scope{Exclude: []string{"kube"}}, "kube-system", true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.scope.Allows(c.ns), c.name)
	}
	assert.True(t, Scope{}.Empty())
	assert.False(t, Scope{Exclude: []string{"a"}}.Empty())
}

func TestNamespaceEntriesAreCheckedAndTidied(t *testing.T) {
	got, err := CleanNames([]string{" Shop ", "shop", "", "kube-*", "a-b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-b", "kube-*", "shop"}, got, "lower-cased, repeats and blanks dropped, sorted")

	for _, bad := range []string{"has space", "a/b", "a*b", "a_b", "x.y"} {
		_, err := CleanNames([]string{bad})
		assert.True(t, errors.Is(err, ErrInvalid), bad)
	}
	many := make([]string, MaxScopeEntries+1)
	for i := range many {
		many[i] = fmt.Sprintf("ns-%d", i)
	}
	_, err = CleanNames(many)
	assert.True(t, errors.Is(err, ErrInvalid), "too many entries")

	assert.Equal(t, []string{}, ParseNames(""))
	assert.Equal(t, []string{}, ParseNames("not json"))
	assert.Equal(t, []string{"a", "b"}, ParseNames(FormatNames([]string{"a", "b"})))
	assert.Equal(t, "", FormatNames(nil))
}

func TestABatchIsRefusedWhenItIsTooBigOrAnEventCannotBeToldApart(t *testing.T) {
	at := time.Now()
	ok := Batch{Events: []Event{{UID: "u1", LastAt: at}}}
	assert.NoError(t, ok.Validate())
	assert.True(t, errors.Is(Batch{Events: []Event{{UID: " ", LastAt: at}}}.Validate(), ErrInvalid), "no uid")
	assert.True(t, errors.Is(Batch{Events: []Event{{UID: "u"}}}.Validate(), ErrInvalid), "no time")
	big := Batch{Events: make([]Event, MaxBatchEvents+1)}
	for i := range big.Events {
		big.Events[i] = Event{UID: "u", LastAt: at}
	}
	assert.True(t, errors.Is(big.Validate(), ErrInvalid), "too many events")
}

func TestClipDoesNotSplitACharacter(t *testing.T) {
	assert.Equal(t, "abc", Clip("abcdef", 3))
	assert.Equal(t, "abc", Clip("abc", 10))
	cut := Clip("ab中文", 4) // 中 is three bytes: cutting at 4 would split it
	assert.Equal(t, "ab", cut)
	assert.Equal(t, cut, strings.ToValidUTF8(cut, ""))
}

func TestSkippedReasonsAreComparedWithoutCase(t *testing.T) {
	assert.True(t, Skipped([]string{"Pulled", " started "}, "pulled"))
	assert.True(t, Skipped([]string{"Pulled", " started "}, "Started"))
	assert.False(t, Skipped([]string{"Pulled"}, "Killing"))
	assert.False(t, Skipped(nil, "Pulled"))
}

func TestTheComponentThatSaidAnEventSaysWhoDidIt(t *testing.T) {
	cases := []struct {
		component, reason string
		origin, category  string
	}{
		{"cluster-autoscaler", "ScaleDown", OriginAuto, "autoscaler"},
		{"horizontal-pod-autoscaler", "SuccessfulRescale", OriginAuto, "autoscaler"},
		{"karpenter", "DisruptionTerminating", OriginAuto, "autoscaler"},
		{"Karpenter-Controller", "Whatever", OriginAuto, "autoscaler"},
		{"kubelet", "Killing", OriginAuto, "kubelet"},
		{"default-scheduler", "FailedScheduling", OriginAuto, "scheduler"},
		{"replicaset-controller", "SuccessfulDelete", OriginAuto, "controller"},
		{"taint-eviction-controller", "TaintManagerEviction", OriginAuto, "controller"},
		{"kube-controller-manager", "x", OriginAuto, "controller"},
		{"cloud-controller-manager", "DeletingNode", OriginAuto, "cloud"},
		{"some-new-thing", "Odd", OriginUnclassified, ""},
		{"", "NoComponent", OriginUnclassified, ""},
	}
	for _, c := range cases {
		got := Classify(nil, Subject{Component: c.component, Reason: c.reason})
		assert.Equal(t, c.origin, got.Origin, c.component)
		assert.Equal(t, c.category, got.Category, c.component)
		assert.Equal(t, got.Origin == OriginUnclassified, got.Rule == "", "an event no rule knows names no rule: "+c.component)
	}
}

func TestNoBuiltInRuleSaysAPersonDidIt(t *testing.T) {
	for _, r := range Builtin() {
		assert.NotEqual(t, OriginManual, r.Origin, "an event does not tell by itself that a person did it: "+r.Name)
	}
}

func TestACustomRuleComesFirstAndAddsACloudOrAPerson(t *testing.T) {
	rules, err := ParseRules(`[
		{"name":"acme-cloud","component":"acme-node-lifecycle","origin":"auto","category":"cloud"},
		{"name":"manual-drain","component":"kubectl*","reason":"Evicted","origin":"manual","category":"operator"},
		{"name":"my-kubelet","component":"kubelet","reason":"Killing","origin":"manual"}
	]`)
	require.NoError(t, err)
	require.Len(t, rules, 3)

	got := Classify(rules, Subject{Component: "acme-node-lifecycle", Reason: "Recycled"})
	assert.Equal(t, Result{Origin: OriginAuto, Category: "cloud", Rule: "acme-cloud"}, got, "a component of another cloud")
	got = Classify(rules, Subject{Component: "kubectl-drain", Reason: "Evicted"})
	assert.Equal(t, OriginManual, got.Origin)
	assert.Equal(t, "manual-drain", got.Rule)
	got = Classify(rules, Subject{Component: "kubelet", Reason: "Killing"})
	assert.Equal(t, "my-kubelet", got.Rule, "a custom rule is tried before the built-in one")
	got = Classify(rules, Subject{Component: "kubelet", Reason: "Pulled"})
	assert.Equal(t, "kubelet", got.Rule, "the built-in rule still says for the rest")
}

func TestBadEventRulesAreRefusedWithAReason(t *testing.T) {
	for _, text := range []string{`not json`, `[{"component":"x","origin":"robot"}]`, `[{"origin":"auto"}]`, `{"component":"x"}`} {
		_, err := ParseRules(text)
		assert.True(t, errors.Is(err, ErrInvalid), text)
	}
	rules, err := ParseRules("  ")
	assert.NoError(t, err)
	assert.Empty(t, rules)
	rules, err = ParseRules(`[{"component":"x","origin":"AUTO"}]`)
	require.NoError(t, err)
	assert.Equal(t, OriginAuto, rules[0].Origin, "the origin is read without regard to case")
	assert.Equal(t, "custom-1", rules[0].Name)
}
