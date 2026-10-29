// Command tsfm-eval measures time-series foundation models served over HTTP (kingfs/unitshub containers) against the platform's own
// algorithms on a COPY of a platform database (work package A9 of P7). Every model is scored with the platform's rolling
// origin backtest on the same cutoffs, so the numbers are comparable with scripts/baseline.
//
//	go run ./scripts/tsfm-eval -db copy.db -models chronos=http://127.0.0.1:18000,timesfm=http://127.0.0.1:18001 -horizon 10
//
// The database is opened read-only and only the copy should be given. The service receives only numbers.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-resource-fcst/pkg/backtest"
	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// remote is a foundation model behind a ready-made UniTS-Hub container (kingfs/unitshub, one model per container). The
// service only predicts: every Forecast call sends the history before the cutoff together with the request, so a cutoff
// never sees its own future and nothing is trained or kept on the other side.
type remote struct {
	kind     string // chronos | timesfm
	label    string
	url      string
	key      string
	quantile float64 // 0 = the mean forecast
	client   *http.Client

	calls       int
	wallSeconds float64
}

func (r *remote) ID() string      { return r.label }
func (r *remote) MinSamples() int { return 30 }

func (r *remote) Forecast(_ context.Context, in forecast.Input, horizon int) ([]float64, error) {
	started := time.Now()
	var body map[string]any
	switch r.kind {
	case "chronos":
		q := r.quantile
		if q == 0 {
			q = 0.5
		}
		body = map[string]any{"task": "forecast_quantile", "input": map[string]any{"series": in.Values, "horizon": horizon, "quantiles": []float64{q}}}
	case "timesfm":
		body = map[string]any{"task": "forecast_point", "input": map[string]any{"history": in.Values, "horizon": horizon}}
	default:
		return nil, fmt.Errorf("unknown model kind %q", r.kind)
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, r.url+"/models/current/invoke", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.key)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invoke: %d %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Output struct {
			Forecasts []struct {
				Mean      []float64            `json:"mean"`
				Quantiles map[string][]float64 `json:"quantiles"`
			} `json:"forecasts"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Output.Forecasts) != 1 {
		return nil, fmt.Errorf("unexpected response: %.120s", string(data))
	}
	r.calls++
	r.wallSeconds += time.Since(started).Seconds()
	f := out.Output.Forecasts[0]
	vals := f.Mean
	if r.quantile > 0 {
		var ok bool
		if vals, ok = f.Quantiles[fmt.Sprintf("%g", r.quantile)]; !ok {
			return nil, fmt.Errorf("the service returned no %g quantile", r.quantile)
		}
	}
	if len(vals) != horizon {
		return nil, fmt.Errorf("asked for %d steps, got %d", horizon, len(vals))
	}
	for i, v := range vals {
		if v < 0 {
			vals[i] = 0 // a resource amount is never negative; the platform's own algorithms clamp the same way
		}
	}
	return vals, nil
}

type row struct {
	module, source, label string
	res                   backtest.Result
	perCall               float64
	note                  string
}

func main() {
	dbPath := flag.String("db", "", "COPY of a platform SQLite database")
	models := flag.String("models", "chronos=http://127.0.0.1:18000,timesfm=http://127.0.0.1:18001", "kind=url of the UniTS-Hub containers to measure")
	key := flag.String("key", "local", "API key of the containers")
	local := flag.String("local", "naive,ewma,holt_winters,theta,ridge_ar,knn,gbdt,hist_percentile,peak_envelope,combination", "platform algorithms to compare with (default parameters)")
	horizon := flag.Int("horizon", 10, "steps predicted from every cutoff")
	cutoffs := flag.Int("cutoffs", 10, "rolling origins")
	only := flag.String("module", "", "only the module with this name")
	flag.Parse()
	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "tsfm-eval: -db is required")
		os.Exit(2)
	}
	if err := run(*dbPath, *key, split(*models), split(*local), *horizon, *cutoffs, *only); err != nil {
		fmt.Fprintln(os.Stderr, "tsfm-eval:", err)
		os.Exit(1)
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type candidate struct {
	m     forecast.Model
	label string
	r     *remote
	alpha float64
}

func (c candidate) model() forecast.Model {
	if c.r != nil {
		return c.r
	}
	return c.m
}

func run(dbPath, key string, models, locals []string, horizon, cutoffs int, only string) error {
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?mode=ro"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	type seriesRow struct {
		ID          uint
		Name        string
		Metric      string
		StepSeconds int
	}
	var all []seriesRow
	if err := db.Raw(`SELECT s.id AS id, m.name AS name, s.metric AS metric, s.step_seconds AS step_seconds
		FROM metric_series s JOIN forecast_module m ON m.id = s.module_id
		WHERE s.metric IN ('cpu','memory') ORDER BY m.name, s.metric`).Scan(&all).Error; err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	var rows []row
	fmt.Printf("# Foundation-model evaluation\n\nDatabase copy of `%s`, %s UTC. Platform rolling-origin backtest (same code as scripts/baseline), horizon %d steps, up to %d cutoffs per series.\n\n", dbPath, time.Now().UTC().Format("2006-01-02 15:04"), horizon, cutoffs)
	for _, s := range all {
		if only != "" && s.Name != only {
			continue
		}
		var samples []model.MetricSample
		if err := db.Where("series_id = ?", s.ID).Order("ts").Find(&samples).Error; err != nil || len(samples) < 60 {
			continue
		}
		pts := make([]collector.Point, len(samples))
		for i, sm := range samples {
			pts[i] = collector.Point{Time: time.Unix(sm.TS, 0).UTC(), Value: sm.Value}
		}
		step := time.Duration(s.StepSeconds) * time.Second
		in, _, ok := collector.ToInput(pts, pts[0].Time, pts[len(pts)-1].Time, step, 5, model.MissingInterpolate)
		if !ok {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s/%s: %d points\n", s.Name, s.Metric, len(in.Values))

		var cands []candidate
		for _, id := range locals {
			m, err := forecast.Build(id, nil)
			if err != nil {
				rows = append(rows, row{module: s.Name, source: s.Metric, label: id, note: "not run: " + oneLine(err.Error())})
				continue
			}
			cands = append(cands, candidate{m: m, label: id, alpha: 0.5})
		}
		for _, spec := range models {
			kind, url, ok := strings.Cut(spec, "=")
			if !ok {
				return fmt.Errorf("-models %q: want kind=url", spec)
			}
			cands = append(cands, candidate{r: &remote{kind: kind, label: kind, url: url, key: key, client: client}, label: kind, alpha: 0.5})
			if kind == "chronos" {
				cands = append(cands, candidate{r: &remote{kind: kind, label: kind + "@p90", url: url, key: key, quantile: 0.9, client: client}, label: kind + "@p90", alpha: 0.9})
			}
		}
		// the same cutoffs for every model: the longest history any of them needs sets the first usable origin
		minTrain := 30
		for _, c := range cands {
			if need := forecast.MinSamplesFor(c.model(), horizon); need > minTrain {
				minTrain = need
			}
		}
		shape := backtest.Config{HorizonSteps: horizon, Cutoffs: cutoffs, Spread: true, SeasonSteps: int(24 * time.Hour / step)}
		origins := backtest.Origins(len(in.Values), shape, minTrain)
		for _, c := range cands {
			cfg := shape
			cfg.Alpha, cfg.Origins = c.alpha, origins
			started := time.Now()
			res, err := backtest.Run(context.Background(), in, c.model(), cfg)
			r := row{module: s.Name, source: s.Metric, label: c.label, res: res}
			if err != nil {
				r.note = "error: " + oneLine(err.Error())
			} else if res.Skipped != "" {
				r.note = "skipped: " + oneLine(res.Skipped)
			}
			if c.r != nil && c.r.calls > 0 {
				r.perCall = c.r.wallSeconds / float64(c.r.calls)
			} else if len(origins) > 0 {
				r.perCall = time.Since(started).Seconds() / float64(len(origins))
			}
			rows = append(rows, r)
		}
	}
	printTables(rows, horizon)
	return nil
}

func printTables(rows []row, horizon int) {
	fmt.Println("## Per series")
	fmt.Println()
	fmt.Println("| module | source | model | cutoffs | points | WAPE | risk WAPE | peak under | coverage | s per forecast | note |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range rows {
		if r.res.Points == 0 {
			fmt.Printf("| %s | %s | %s | | | | | | | | %s |\n", r.module, r.source, r.label, r.note)
			continue
		}
		fmt.Printf("| %s | %s | %s | %d | %d | %.3f | %.3f | %.3f | %.3f | %.3f | %s |\n", r.module, r.source, r.label,
			r.res.Cutoffs, r.res.Points, r.res.WAPE, r.res.RiskWAPE, r.res.PeakUnder, r.res.Coverage, r.perCall, r.note)
	}
	type agg struct {
		n                                int
		wape, risk, peak, cover, perCall float64
	}
	byModel := map[string]*agg{}
	var order []string
	for _, r := range rows {
		if r.res.Points == 0 {
			continue
		}
		a := byModel[r.label]
		if a == nil {
			a = &agg{}
			byModel[r.label] = a
			order = append(order, r.label)
		}
		a.n++
		a.wape += r.res.WAPE
		a.risk += r.res.RiskWAPE
		a.peak += r.res.PeakUnder
		a.cover += r.res.Coverage
		a.perCall += r.perCall
	}
	sort.Slice(order, func(i, j int) bool {
		return byModel[order[i]].risk/float64(byModel[order[i]].n) < byModel[order[j]].risk/float64(byModel[order[j]].n)
	})
	fmt.Printf("\n## Mean over series (horizon %d), ranked by risk WAPE\n\n", horizon)
	fmt.Println("| model | series | WAPE | risk WAPE | peak under | coverage | s per forecast |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, id := range order {
		a := byModel[id]
		n := float64(a.n)
		fmt.Printf("| %s | %d | %.3f | %.3f | %.3f | %.3f | %.3f |\n", id, a.n, a.wape/n, a.risk/n, a.peak/n, a.cover/n, a.perCall/n)
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return strings.ReplaceAll(s, "|", "/")
}
