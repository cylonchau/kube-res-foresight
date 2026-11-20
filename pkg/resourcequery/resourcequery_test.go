package resourcequery

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeConfig map[string]string

func (f fakeConfig) GetValueOrDefault(_ context.Context, key, fallback string) (string, error) {
	if v, ok := f[key]; ok {
		return v, nil
	}
	return fallback, nil
}

func balanced(s string) bool {
	depth := map[rune]int{}
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	var stack []rune
	inString := false
	for _, r := range s {
		switch {
		case r == '"':
			inString = !inString
		case inString:
		case r == '(' || r == '[' || r == '{':
			stack = append(stack, r)
			depth[r]++
		case r == ')' || r == ']' || r == '}':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[r] {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0 && !inString
}

func TestEveryQueryOfTheSetIsNamedOnceGroupedAndWritten(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{GroupVariable: true}
	for _, g := range Groups {
		groups[g] = true
	}
	for _, q := range Queries {
		assert.False(t, seen[q.Name], "%s is there twice", q.Name)
		seen[q.Name] = true
		assert.True(t, groups[q.Group], "%s has a group that is not one", q.Name)
		assert.NotEmpty(t, q.Title, q.Name)
		if q.Group != GroupVariable {
			assert.NotEmpty(t, q.Unit, q.Name)
		}
		assert.NotEmpty(t, q.Default, q.Name)
		assert.True(t, balanced(q.Default), "%s: the brackets and quotes of %s are not balanced", q.Name, q.Default)
		assert.Equal(t, strings.Contains(q.Default, "sum by") || q.Name == "node_ready", q.Legend != "", "%s: a query that gives a series for each one has a legend, one that gives one figure has none", q.Name)
	}
	for _, g := range Groups {
		n := 0
		for _, q := range Queries {
			if q.Group == g {
				n++
			}
		}
		assert.Positive(t, n, "the group %s has no query", g)
	}
}

func TestTheSystemParameterReplacesADefaultAndAnEmptyOneLeavesIt(t *testing.T) {
	set := Load(context.Background(), fakeConfig{Key("cluster_used_cpu"): "  sum(rate(my_cpu_total[1m]))  ", Key("cluster_pods"): "   "})
	assert.Equal(t, "sum(rate(my_cpu_total[1m]))", set["cluster_used_cpu"])
	def, _ := Find("cluster_pods")
	assert.Equal(t, def.Default, set["cluster_pods"], "a parameter with nothing in it is the default")
	assert.Len(t, set, len(Queries))
	assert.Equal(t, Defaults()["cluster_nodes"], set["cluster_nodes"])
}

func TestTheVariablesAreFilledWithTheValuesGivenAndWithEveryOneWhenTheyAreNot(t *testing.T) {
	q := `sum(rate(x{namespace=~"$namespace",pod=~"$workload-.*",node=~"${node}"}[5m]))`
	assert.Equal(t, `sum(rate(x{namespace=~".*",pod=~".*-.*",node=~".*"}[5m]))`, Expand(q, nil), "the platform asks for all of them")
	assert.Equal(t, `sum(rate(x{namespace=~"prod",pod=~"shop-.*",node=~"n1"}[5m]))`, Expand(q, map[string]string{"namespace": "prod", "workload": "shop", "node": "n1"}))
	assert.ElementsMatch(t, []string{"namespace", "workload", "node"}, Used(q))
	assert.Empty(t, Used(`count(kube_node_info)`))
	assert.Equal(t, `a ".*" b`, Expand(`a "$pod" b`, map[string]string{"pod": ""}), "an empty value is all")
}

func TestTheParametersAreSeededWithTheirDefaultsAndSayWhatTheyAre(t *testing.T) {
	configs := Configs()
	require.Len(t, configs, len(Queries))
	for _, c := range configs {
		assert.True(t, strings.HasPrefix(c.ConfigKey, "query."), c.ConfigKey)
		assert.NotEmpty(t, c.ConfigValue)
		assert.Contains(t, c.Remark, "default")
		assert.NotContains(t, strings.ToLower(c.Remark+c.ConfigName), "kube-state-metrics", "a metric name is a default of the query, never named as the source")
	}
}
