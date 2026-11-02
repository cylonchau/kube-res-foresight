package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
)

// Every tool reads. None writes, none runs a forecast, none reaches the cluster: the assistant can only look at what the platform has
// stored. What a tool returns is data for the model to describe, never an instruction to it.

// Attachment is a file the user can download after a tool made it available.
type Attachment struct {
	Name string `json:"name"`
	// Path is a path of the platform's own API (it needs the user's login to be fetched).
	Path   string `json:"path"`
	Format string `json:"format"`
}

// toolResult is what a tool gives back: the data for the model, and optionally a file for the user.
type toolResult struct {
	Data       any
	Attachment *Attachment
}

type tool struct {
	spec ToolSpec
	run  func(ctx context.Context, args json.RawMessage) (toolResult, error)
}

func object(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

var (
	hoursProp  = map[string]any{"type": "integer", "description": "How many hours back to look, 1 to 720 (default 24)."}
	limitProp  = map[string]any{"type": "integer", "description": "How many rows at most, 1 to 50 (default 20)."}
	moduleProp = map[string]any{"type": "string", "description": "The name of a module, as list_modules gives it. Leave out for all modules."}
)

func clamp(n, lo, hi, fallback int) int {
	if n == 0 {
		return fallback
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func parse(args json.RawMessage, into any) error {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("the arguments are not valid JSON for this tool: %w", err)
	}
	return nil
}

// tools is the set the model may call: the read-only ones, and, for a question asked by a signed-in user, those that make and manage that
// user's scheduled tasks. A scheduled run has no user, so a task cannot make more tasks.
func (s *Service) tools(ctx context.Context) []tool {
	set := s.readTools()
	if _, ok := ownerOf(ctx); ok && s.Tasks != nil {
		set = append(set, s.taskTools()...)
	}
	return set
}

func (s *Service) readTools() []tool {
	return []tool{
		{
			spec: ToolSpec{Name: "get_service_status", Description: "The state of the whole platform now: its version and uptime, the switches that open the auto and admission modes, how many modules there are by mode, which modules need a look and why, what the operator applied (scale-ups, scale-downs), failed forecast runs, what the operator last reported, and the background tasks. Start here for any question about how the service is doing.",
				Parameters: object(map[string]any{"hours": hoursProp})},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct{ Hours int }
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				r, err := s.Status(ctx, clamp(a.Hours, 1, forecastsvc.MaxEffectHours, 24))
				return toolResult{Data: r}, err
			},
		},
		{
			spec: ToolSpec{Name: "list_modules", Description: "Every module (a workload the platform forecasts and may scale): its name, namespace, workload, mode, whether it is enabled, in bypass, and forecasts.",
				Parameters: object(map[string]any{})},
			run: func(ctx context.Context, _ json.RawMessage) (toolResult, error) {
				mods, err := s.Modules.AllModules(ctx)
				if err != nil {
					return toolResult{}, err
				}
				type row struct {
					Name       string `json:"name"`
					Namespace  string `json:"namespace"`
					Workload   string `json:"workload"`
					Kind       string `json:"kind"`
					Mode       string `json:"mode"`
					Enabled    bool   `json:"enabled"`
					Bypass     bool   `json:"bypass"`
					Predicting bool   `json:"predicting"`
				}
				out := make([]row, 0, len(mods))
				for _, m := range mods {
					out = append(out, row{Name: m.Name, Namespace: m.Namespace, Workload: m.Workload, Kind: m.WorkloadKind, Mode: m.Mode, Enabled: m.Enabled, Bypass: m.Bypass, Predicting: m.Predicts()})
				}
				return toolResult{Data: out}, nil
			},
		},
		{
			spec: ToolSpec{Name: "get_module_effect_report", Description: "What the forecasts of one module were worth over a window: accuracy, whether scale-ups were in time, capacity wasted or missing (Pod-hours and the capacity SLO), how steady the replicas were, who decided and whether the module's setting works.",
				Parameters: object(map[string]any{"module": moduleProp, "hours": hoursProp}, "module")},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct {
					Module string
					Hours  int
				}
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				m, err := s.Modules.ByName(ctx, strings.TrimSpace(a.Module))
				if err != nil {
					return toolResult{}, fmt.Errorf("no module is named %q: call list_modules for the names", a.Module)
				}
				r, err := s.Forecast.EffectReport(ctx, m.ID, clamp(a.Hours, 1, forecastsvc.MaxEffectHours, 24))
				return toolResult{Data: r}, err
			},
		},
		{
			spec: ToolSpec{Name: "list_scaling_actions", Description: "The scaling actions the operator reported, newest first: when, the module and workload, the replicas before and after, what it did (ScaleUp, ScaleDown, None), the result (applied, observed, held, conflict, error), what the replicas followed (decision, cache, fallback, frozen), the state of the module in an outage of the platform, and the reason. Late reports were kept while the platform was away.",
				Parameters: object(map[string]any{"module": moduleProp, "result": map[string]any{"type": "string", "enum": []string{"applied", "observed", "held", "conflict", "error"}, "description": "Only this result."}, "hours": hoursProp, "limit": limitProp})},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct {
					Module, Result string
					Hours, Limit   int
				}
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				f := rdb.ExecutionFilter{Result: a.Result, Sort: "created_at", Dir: "desc"}
				since := s.now().Add(-time.Duration(clamp(a.Hours, 1, forecastsvc.MaxEffectHours, 24)) * time.Hour)
				f.Since = &since
				names, id, err := s.moduleFilter(ctx, a.Module)
				if err != nil {
					return toolResult{}, err
				}
				f.ModuleID = id
				rows, total, err := s.Predictions.SearchExecutions(ctx, f, 1, clamp(a.Limit, 1, 50, 20))
				if err != nil {
					return toolResult{}, err
				}
				type row struct {
					At      time.Time `json:"at"`
					Module  string    `json:"module"`
					Target  string    `json:"target"`
					Current int       `json:"current"`
					Desired int       `json:"desired"`
					Action  string    `json:"action"`
					Result  string    `json:"result"`
					Source  string    `json:"followed,omitempty"`
					State   string    `json:"state,omitempty"`
					Late    bool      `json:"reported_late,omitempty"`
					Reason  string    `json:"reason,omitempty"`
				}
				out := make([]row, 0, len(rows))
				for _, e := range rows {
					late := e.ReportedAt != nil && e.ReportedAt.Sub(e.CreatedAt) > lateAfter
					out = append(out, row{At: e.CreatedAt, Module: names[e.ModuleID], Target: e.Namespace + "/" + e.TargetKind + "/" + e.TargetName, Current: e.Current, Desired: e.Desired,
						Action: e.Action, Result: e.Result, Source: e.DecisionSource, State: e.DegradeState, Late: late, Reason: clip(e.Reason, 200)})
				}
				return toolResult{Data: map[string]any{"total": total, "shown": len(out), "actions": out}}, nil
			},
		},
		{
			spec: ToolSpec{Name: "list_admissions", Description: "The admission records, newest first: the requests Kyverno asked the platform about when a workload was created or updated, whether the platform's answer was applied, the CPU and memory it gave against what the Pod asked for, and why.",
				Parameters: object(map[string]any{"module": moduleProp, "applied": map[string]any{"type": "boolean", "description": "true: only the answers that were applied; false: only the refusals."}, "hours": hoursProp, "limit": limitProp})},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct {
					Module       string
					Applied      *bool
					Hours, Limit int
				}
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				since := s.now().Add(-time.Duration(clamp(a.Hours, 1, forecastsvc.MaxEffectHours, 24)) * time.Hour)
				names, id, err := s.moduleFilter(ctx, a.Module)
				if err != nil {
					return toolResult{}, err
				}
				rows, total, err := s.Predictions.SearchAdmissions(ctx, rdb.AdmissionFilter{ModuleID: id, Applied: a.Applied, Since: &since, Sort: "created_at", Dir: "desc"}, 1, clamp(a.Limit, 1, 50, 20))
				if err != nil {
					return toolResult{}, err
				}
				type row struct {
					At            time.Time `json:"at"`
					Module        string    `json:"module"`
					Workload      string    `json:"workload"`
					Origin        string    `json:"origin,omitempty"`
					Applied       bool      `json:"applied"`
					CPUMilli      int64     `json:"cpu_milli,omitempty"`
					MemoryBytes   int64     `json:"memory_bytes,omitempty"`
					OriginalCPU   int64     `json:"original_cpu_milli,omitempty"`
					OriginalMem   int64     `json:"original_memory_bytes,omitempty"`
					Reason        string    `json:"reason,omitempty"`
					RequestUIDSet bool      `json:"has_request_uid"`
				}
				out := make([]row, 0, len(rows))
				for _, d := range rows {
					out = append(out, row{At: d.CreatedAt, Module: names[d.ModuleID], Workload: d.Namespace + "/" + d.Workload, Origin: d.Origin, Applied: d.Applied, CPUMilli: d.CPUMilli, MemoryBytes: d.MemoryBytes,
						OriginalCPU: d.OriginalCPUMilli, OriginalMem: d.OriginalMemoryBytes, Reason: clip(d.Reason, 200), RequestUIDSet: d.RequestUID != nil})
				}
				return toolResult{Data: map[string]any{"total": total, "shown": len(out), "admissions": out}}, nil
			},
		},
		{
			spec: ToolSpec{Name: "export_status_report", Description: "Make the service's running state available to the user as a file they can download (Markdown to read and keep, or JSON for other tools). Call it when the user asks to export, save, download or send the service status. The user gets a download button under your reply; tell them it is there.",
				Parameters: object(map[string]any{"format": map[string]any{"type": "string", "enum": []string{"markdown", "json"}, "description": "markdown (default) or json."}, "hours": hoursProp})},
			run: func(ctx context.Context, args json.RawMessage) (toolResult, error) {
				var a struct {
					Format string
					Hours  int
				}
				if err := parse(args, &a); err != nil {
					return toolResult{}, err
				}
				format := "markdown"
				if strings.EqualFold(a.Format, "json") {
					format = "json"
				}
				hours := clamp(a.Hours, 1, forecastsvc.MaxEffectHours, 24)
				r, err := s.Status(ctx, hours)
				if err != nil {
					return toolResult{}, err
				}
				att := ExportAttachment(format, hours, "", s.now())
				return toolResult{Attachment: &att, Data: map[string]any{
					"download": "A download button for " + att.Name + " is shown to the user under your reply.",
					"summary":  map[string]any{"modules": r.Modules, "scale_ups": r.Activity.ScaleUps, "scale_downs": r.Activity.ScaleDowns, "failed_forecast_runs": r.Activity.FailedRuns},
				}}, nil
			},
		},
	}
}

// ExportAttachment names the download of the status report: the file name and the path of the API that makes it. lang is "zh" or "en"
// (empty leaves it to the server's default).
func ExportAttachment(format string, hours int, lang string, at time.Time) Attachment {
	ext := "md"
	if format == "json" {
		ext = "json"
	}
	q := url.Values{"format": {format}, "hours": {fmt.Sprint(hours)}}
	if lang != "" {
		q.Set("lang", lang)
	}
	return Attachment{Name: fmt.Sprintf("service-status-%s.%s", at.UTC().Format("20060102-1504"), ext), Path: "/api/v1/assistant/export?" + q.Encode(), Format: format}
}

// moduleFilter resolves an optional module name to its id (0 for all) and returns the names of every module by id.
func (s *Service) moduleFilter(ctx context.Context, name string) (map[uint]string, uint, error) {
	mods, err := s.Modules.AllModules(ctx)
	if err != nil {
		return nil, 0, err
	}
	names := make(map[uint]string, len(mods))
	var id uint
	name = strings.TrimSpace(name)
	for _, m := range mods {
		names[m.ID] = m.Name
		if name != "" && m.Name == name {
			id = m.ID
		}
	}
	if name != "" && id == 0 {
		return nil, 0, errors.New("no module is named " + fmt.Sprintf("%q", name) + ": call list_modules for the names")
	}
	return names, id, nil
}
