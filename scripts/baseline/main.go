// Command baseline replays a rolling-origin backtest of every enabled algorithm of every enabled module on a COPY of a
// platform database and prints one markdown table: the measured baseline of the algorithms on that data (work package
// A0 of P7). The original database is never opened for writing; the backtest rows land in the copy and are deleted
// with it.
//
//	go run ./scripts/baseline -db kube-resource-fcst.db [-cutoffs 5] [-module load-web]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kube-resource-fcst/pkg/backtest"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
)

func main() {
	dbPath := flag.String("db", "kube-resource-fcst.db", "platform SQLite database (copied, not modified)")
	cutoffs := flag.Int("cutoffs", 0, "rolling origins per algorithm (0 = the platform default)")
	only := flag.String("module", "", "only the module with this name")
	var sets, enables setFlag
	flag.Var(&enables, "enable", "algorithm to switch on in the copy (an algorithm added since the data was saved is off until someone enables it); repeatable")
	flag.Var(&sets, "set", "algorithm=JSON parameters to apply to the copy before the run, e.g. ewma={\"quantile_level\":0.9}; repeatable")
	flag.Parse()
	if err := run(*dbPath, *cutoffs, *only, sets, enables); err != nil {
		fmt.Fprintln(os.Stderr, "baseline:", err)
		os.Exit(1)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// setFlag collects the repeatable -set algorithm=JSON arguments.
type setFlag []string

func (s *setFlag) String() string     { return strings.Join(*s, " ") }
func (s *setFlag) Set(v string) error { *s = append(*s, v); return nil }

func run(dbPath string, cutoffs int, only string, sets, enables setFlag) error {
	dir, err := os.MkdirTemp("", "baseline-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	work := filepath.Join(dir, "copy.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(dbPath+suffix, work+suffix); err != nil && suffix == "" {
			return err
		}
	}
	db, err := gorm.Open(sqlite.Open(work), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	modules := rdb.NewModuleDAO(db)
	svc := forecastsvc.New(modules, rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))
	ctx := context.Background()
	// the platform refuses stale history, so the clock stands at the newest sample of the copy: the run is the same
	// whenever it is made, and an old snapshot can still be measured
	var newest int64
	if err := db.Raw("SELECT COALESCE(MAX(ts), 0) FROM metric_sample").Scan(&newest).Error; err == nil && newest > 0 {
		at := time.Unix(newest, 0).UTC().Add(30 * time.Second)
		svc.Now = func() time.Time { return at }
	}
	if err := svc.EnsureAlgorithms(ctx); err != nil {
		return err
	}
	for _, kv := range sets {
		id, raw, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("-set %q: want algorithm=JSON", kv)
		}
		canonical, err := forecastsvc.ValidateParams(id, raw)
		if err != nil {
			return fmt.Errorf("-set %s: %w", id, err)
		}
		cfg, err := svc.Algorithms.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("-set %s: %w", id, err)
		}
		cfg.Params = canonical
		if err := svc.Algorithms.Save(ctx, cfg); err != nil {
			return err
		}
	}
	for _, id := range enables {
		cfg, err := svc.Algorithms.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("-enable %s: %w", id, err)
		}
		cfg.Enabled = true
		if err := svc.Algorithms.Save(ctx, cfg); err != nil {
			return err
		}
	}
	list, err := modules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("# Backtest baseline\n\nDatabase copy of `%s`, %s UTC. Rolling-origin backtest, the platform's own code path; the numbers describe this data only.\n\n", dbPath, time.Now().UTC().Format("2006-01-02 15:04"))
	fmt.Println("| module | source | step | history | horizon | algorithm | status | cutoffs | points | WAPE | risk WAPE | peak under | coverage | baseline | baseline WAPE | baseline risk | reason |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, mod := range list {
		if only != "" && mod.Name != only {
			continue
		}
		sources, err := modules.Sources(ctx, mod.ID)
		if err != nil {
			return err
		}
		for _, src := range sources {
			rows, err := svc.Backtest(ctx, mod.ID, src.Key, cutoffs)
			if err != nil {
				fmt.Printf("| %s | %s | | | | - | error: %s | | | | | | | | | | |\n", mod.Name, src.Key, oneLine(err.Error()))
				continue
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].AlgorithmID < rows[j].AlgorithmID })
			for _, r := range rows {
				var m, b backtest.Result
				_ = json.Unmarshal([]byte(r.Metrics), &m)
				_ = json.Unmarshal([]byte(r.BaselineRes), &b)
				status := "scored"
				switch {
				case m.Skipped != "":
					status = "skipped: " + oneLine(m.Skipped)
				case r.Promoted:
					status = "promoted"
				}
				history := fmt.Sprintf("%.1fh", r.WindowEnd.Sub(r.WindowStart).Hours())
				fmt.Printf("| %s | %s | %ds | %s | %d | %s | %s | %d | %d | %.3f | %s | %.3f | %.3f | %s | %.3f | %s | %s |\n", mod.Name, src.Key, r.StepSeconds, history, r.HorizonSteps,
					r.AlgorithmID, status, m.Cutoffs, m.Points, m.WAPE, riskText(m), m.PeakUnder, m.Coverage, r.BaselineID, b.WAPE, riskText(b), oneLine(r.Reason))
			}
		}
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return strings.ReplaceAll(s, "|", "/")
}

// riskText is the underestimate-weighted error, or a dash for a result stored without it.
func riskText(r backtest.Result) string {
	if r.UnderWeight == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", r.RiskWAPE)
}
