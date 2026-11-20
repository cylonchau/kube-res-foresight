package grafana

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dash struct {
	UID        string   `json:"uid"`
	Title      string   `json:"title"`
	Tags       []string `json:"tags"`
	Schema     int      `json:"schemaVersion"`
	Templating struct {
		List []struct {
			Name       string         `json:"name"`
			Type       string         `json:"type"`
			Query      any            `json:"query"`
			Datasource map[string]any `json:"datasource"`
			AllValue   string         `json:"allValue"`
		} `json:"list"`
	} `json:"templating"`
	Panels []panel          `json:"panels"`
	Links  []map[string]any `json:"links"`
}

type panel struct {
	ID         int            `json:"id"`
	Type       string         `json:"type"`
	Title      string         `json:"title"`
	Datasource map[string]any `json:"datasource"`
	GridPos    struct {
		H, W, X, Y int
	} `json:"gridPos"`
	Targets []struct {
		RefID        string         `json:"refId"`
		Expr         string         `json:"expr"`
		LegendFormat string         `json:"legendFormat"`
		Instant      bool           `json:"instant"`
		Datasource   map[string]any `json:"datasource"`
	} `json:"targets"`
	Links []map[string]any `json:"links"`
}

func build(t *testing.T, o Options) dash {
	t.Helper()
	raw, err := JSON(o)
	require.NoError(t, err)
	var d dash
	require.NoError(t, json.Unmarshal(raw, &d))
	return d
}

var variableUse = regexp.MustCompile(`\$\{?([a-z_]+)`)

func TestTheDashboardIsOneValidDashboardWithItsVariablesAndItsRows(t *testing.T) {
	d := build(t, Options{})
	assert.Equal(t, DefaultUID, d.UID)
	assert.Equal(t, 39, d.Schema)
	assert.Contains(t, d.Tags, "kube-res-foresight")

	names := map[string]bool{}
	for _, v := range d.Templating.List {
		names[v.Name] = true
		if v.Name != "datasource" && v.Name != "cluster" {
			assert.Equal(t, "${datasource}", v.Datasource["uid"], "%s is asked of the data source picked above", v.Name)
			assert.Equal(t, ".*", v.AllValue, "%s: 'all' is a regular expression that takes every one, which is what the platform fills a variable with too", v.Name)
		}
	}
	for _, want := range []string{"datasource", "cluster", "namespace", "workload", "pod", "node", "container"} {
		assert.True(t, names[want], "the variable %s", want)
	}

	var rowTitles []string
	for _, p := range d.Panels {
		if p.Type == "row" {
			rowTitles = append(rowTitles, p.Title)
		}
	}
	assert.Equal(t, []string{"Cluster", "Nodes", "Workload", "Pods", "Containers", "JVM"}, rowTitles, "the rows are the ones the requirement names, in order")
}

func TestEveryPanelAsksThePlatformsQueriesOfThePickedDataSourceAndIsPlacedInTheGrid(t *testing.T) {
	d := build(t, Options{})
	ids := map[int]bool{}
	type cell struct{ x, y int }
	taken := map[cell]string{}
	vars := map[string]bool{"datasource": true}
	for _, v := range d.Templating.List {
		vars[v.Name] = true
	}
	var leaves int
	for _, p := range d.Panels {
		assert.False(t, ids[p.ID], "the id %d is used twice", p.ID)
		ids[p.ID] = true
		assert.NotEmpty(t, p.Title)
		assert.GreaterOrEqual(t, p.GridPos.X, 0)
		assert.LessOrEqual(t, p.GridPos.X+p.GridPos.W, 24, "%s is inside the 24 columns", p.Title)
		assert.Positive(t, p.GridPos.W)
		assert.Positive(t, p.GridPos.H)
		for x := p.GridPos.X; x < p.GridPos.X+p.GridPos.W; x++ {
			for y := p.GridPos.Y; y < p.GridPos.Y+p.GridPos.H; y++ {
				if other, found := taken[cell{x, y}]; found {
					t.Fatalf("%q overlaps %q at (%d,%d)", p.Title, other, x, y)
				}
				taken[cell{x, y}] = p.Title
			}
		}
		if p.Type == "row" {
			continue
		}
		leaves++
		assert.Contains(t, []string{"stat", "timeseries"}, p.Type, p.Title)
		assert.Equal(t, "${datasource}", p.Datasource["uid"], p.Title)
		require.NotEmpty(t, p.Targets, p.Title)
		refs := map[string]bool{}
		for _, tg := range p.Targets {
			assert.False(t, refs[tg.RefID], "%s: refId %s twice", p.Title, tg.RefID)
			refs[tg.RefID] = true
			assert.NotEmpty(t, strings.TrimSpace(tg.Expr), p.Title)
			assert.Equal(t, "${datasource}", tg.Datasource["uid"], p.Title)
			assert.NotContains(t, tg.Expr, "{{", "a query has no template of the platform left in it")
			assert.Equal(t, strings.Count(tg.Expr, "("), strings.Count(tg.Expr, ")"), "%s: the brackets of %s", p.Title, tg.Expr)
			assert.Equal(t, strings.Count(tg.Expr, "{"), strings.Count(tg.Expr, "}"), p.Title)
			assert.Equal(t, 0, strings.Count(tg.Expr, `"`)%2, p.Title)
			for _, m := range variableUse.FindAllStringSubmatch(tg.Expr, -1) {
				if m[1] == "__all" || m[1] == "__interval" || m[1] == "__rate_interval" {
					continue
				}
				assert.True(t, vars[m[1]], "%s uses $%s, which the dashboard has no variable for", p.Title, m[1])
			}
			if p.Type == "stat" {
				assert.True(t, tg.Instant, "%s: a figure is the value now", p.Title)
			} else {
				assert.Contains(t, []string{"", "allocatable", "requested", "used", "requests", "limits", "committed", "max", "nodes", "ready", "{{node}}", "{{pod}}", "{{container}}"}, tg.LegendFormat, p.Title)
			}
		}
	}
	assert.Greater(t, leaves, 25)
}

func TestEveryQueryOfTheSharedSetIsInThePanelsOrTheVariablesSoThePagesAndTheDashboardAskTheSameThings(t *testing.T) {
	d := build(t, Options{})
	used := map[string]bool{}
	set := resourcequery.Defaults()
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			used[tg.Expr] = true
		}
	}
	for _, v := range d.Templating.List {
		if q, ok := v.Query.(map[string]any); ok {
			used[q["query"].(string)] = true
		}
	}
	for _, q := range resourcequery.Queries {
		assert.True(t, used[set[q.Name]], "the query %s is not in the dashboard", q.Name)
	}
}

func TestAQueryTheSystemRewroteIsTheOneInThePanelAndNothingElseChanges(t *testing.T) {
	mine := `sum(rate(my_container_cpu_total{ns=~"$namespace"}[1m]))`
	set := resourcequery.Defaults()
	set["workload_cpu_usage"] = mine
	d := build(t, Options{Queries: set})
	found := 0
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			if tg.Expr == mine {
				found++
				assert.Contains(t, p.Title, "CPU: requests, limits, used", "the panel that draws the workload CPU")
			}
		}
	}
	assert.Equal(t, 1, found)
	base := build(t, Options{})
	assert.Equal(t, len(base.Panels), len(d.Panels))
}

func TestTheClusterNameIsTheDefaultOfTheClusterVariableAndTheLinksBackToThePlatformAreThereOnlyWhenItsAddressIsKnown(t *testing.T) {
	without := build(t, Options{Cluster: "prod-east"})
	assert.Empty(t, without.Links)
	for _, p := range without.Panels {
		assert.Empty(t, p.Links, p.Title)
	}
	raw, err := JSON(Options{Cluster: "prod-east"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"value": "prod-east"`)

	with := build(t, Options{PlatformURL: "https://fcst.example.com/"})
	require.Len(t, with.Links, 1)
	assert.Equal(t, "https://fcst.example.com"+PlatformPage, with.Links[0]["url"], "no double slash")
	linked := 0
	for _, p := range with.Panels {
		for _, l := range p.Links {
			linked++
			url := l["url"].(string)
			assert.True(t, strings.HasPrefix(url, "https://fcst.example.com"+PlatformPage+"?namespace=${namespace:raw}&workload=${workload:raw}"), url)
		}
	}
	assert.Equal(t, 3, linked, "the panels of a workload that say what it asks for and what it uses")
}

func TestTheDashboardIsTheSameEveryTimeItIsBuilt(t *testing.T) {
	a, err := JSON(Options{})
	require.NoError(t, err)
	b, err := JSON(Options{})
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b))
	assert.True(t, strings.HasSuffix(string(a), "}\n"))
}

// The files that are shipped (the folder of the dashboards and the Helm chart) are what the generator writes: change a query or a panel and run
// go run ./pkg/grafana/gen, or this fails.
func TestTheShippedDashboardsAreWhatTheGeneratorWrites(t *testing.T) {
	want, err := JSON(Options{})
	require.NoError(t, err)
	for _, rel := range []string{"deploy/grafana/" + ShippedName, "deploy/helm/kube-res-foresight/files/resources-dashboard.json"} {
		got, err := os.ReadFile(filepath.Join("..", "..", rel))
		require.NoError(t, err, rel)
		assert.Equal(t, string(want), strings.ReplaceAll(string(got), "\r\n", "\n"), "%s is not what go run ./pkg/grafana/gen writes", rel)
	}
}
