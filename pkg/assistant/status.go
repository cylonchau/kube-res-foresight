package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/version"
)

// StatusReport is what the platform is doing now, from the facts it has stored: the one snapshot the assistant's tools, the export and the
// user's questions about "how is the service" all read. It changes nothing and holds no secret: only counts, names, times and the
// non-secret switches.
type StatusReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	// Hours is the window the counts cover.
	Hours    int                         `json:"hours"`
	Service  ServiceInfo                 `json:"service"`
	Switches map[string]bool             `json:"switches"`
	Modules  forecastsvc.DashboardCounts `json:"modules"`
	// Attention are the modules that need a look, worst first (see the home page).
	Attention []forecastsvc.DashboardModule `json:"attention"`
	Activity  Activity                      `json:"activity"`
	Operator  OperatorStatus                `json:"operator"`
	Jobs      JobStatus                     `json:"jobs"`
}

// ServiceInfo is the platform process.
type ServiceInfo struct {
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Database      string    `json:"database"`
}

// Activity is what the operator applied and what failed in the window.
type Activity struct {
	ScaleUps   int   `json:"scale_ups"`
	ScaleDowns int   `json:"scale_downs"`
	FailedRuns int64 `json:"failed_forecast_runs"`
}

// OperatorStatus is what the platform knows of the operator. It does not watch the operator: it knows when it last heard from it.
type OperatorStatus struct {
	// LastSeenAt is the last time the operator read its modules or settings, counted since this platform process started (nil when it has
	// not yet); LastReportAt is the newest report of a scaling action it sent.
	LastSeenAt   *time.Time `json:"last_seen_at"`
	LastReportAt *time.Time `json:"last_report_at"`
	// ReportsInWindow counts the operator's reports (scaling actions, holds, conflicts, errors) by result and by the state the module was
	// in; Late are the ones that reached the platform more than a minute after the action (it was kept while the platform was away).
	ReportsInWindow int64            `json:"reports_in_window"`
	ByResult        map[string]int64 `json:"by_result"`
	ByState         map[string]int64 `json:"by_state"`
	Late            int64            `json:"late_reports"`
	MaxLateSeconds  int64            `json:"max_late_seconds"`
}

// JobStatus is the background tasks (collection, forecast, scoring, clean-up).
type JobStatus struct {
	ByStatus       map[string]int64 `json:"by_status"`
	RecentFailures []JobFailure     `json:"recent_failures"`
}

// JobFailure is one failed task.
type JobFailure struct {
	ID       int64      `json:"id"`
	Type     string     `json:"type"`
	Target   string     `json:"target,omitempty"`
	Error    string     `json:"error,omitempty"`
	FinishAt *time.Time `json:"finished_at,omitempty"`
}

var operatorSeen atomic.Pointer[time.Time]

// MarkOperatorSeen records that the operator has just asked the platform for something.
func MarkOperatorSeen(at time.Time) { operatorSeen.Store(&at) }

// switchKeys are the parameters that open the modes writing to the cluster, and the assistant itself: the non-secret switches a status
// report states.
var switchKeys = []string{"forecast.allow_admission_mode", "forecast.allow_auto_mode", KeyEnabled}

const (
	// lateAfter is how long after an action a report has to arrive to count as late.
	lateAfter = time.Minute
	// maxReportRows bounds the rows read to find the late reports.
	maxReportRows = 5000
	maxFailures   = 5
)

// Status builds the snapshot of the last hours (24 when 0, at most forecastsvc.MaxEffectHours).
func (s *Service) Status(ctx context.Context, hours int) (*StatusReport, error) {
	if hours <= 0 {
		hours = forecastsvc.DefaultDelayHours
	}
	if hours > forecastsvc.MaxEffectHours {
		hours = forecastsvc.MaxEffectHours
	}
	now := s.now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	out := &StatusReport{GeneratedAt: now, Hours: hours, Switches: map[string]bool{}}
	out.Service = ServiceInfo{Version: version.Version, StartedAt: s.Started, UptimeSeconds: int64(now.Sub(s.Started).Seconds()), Database: s.Database}
	for _, key := range switchKeys {
		on, err := s.Config.GetBool(ctx, key, false)
		if err != nil {
			return nil, err
		}
		out.Switches[key] = on
	}

	dash, err := s.Forecast.Dashboard(ctx, hours)
	if err != nil {
		return nil, err
	}
	out.Modules, out.Attention = dash.Modules, dash.Attention
	out.Activity = Activity{ScaleUps: dash.Totals.ScaleUps, ScaleDowns: dash.Totals.ScaleDowns, FailedRuns: dash.Totals.FailedRuns}

	if err := s.operatorStatus(ctx, since, &out.Operator); err != nil {
		return nil, err
	}
	if err := s.jobStatus(ctx, since, &out.Jobs); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) operatorStatus(ctx context.Context, since time.Time, out *OperatorStatus) error {
	out.ByResult, out.ByState = map[string]int64{}, map[string]int64{}
	out.LastSeenAt = operatorSeen.Load()
	var last model.ScalingExecution
	switch err := s.DB.WithContext(ctx).Order("id DESC").Limit(1).Find(&last).Error; {
	case err != nil:
		return err
	case last.ID != 0:
		at := last.CreatedAt
		if last.ReportedAt != nil {
			at = *last.ReportedAt
		}
		out.LastReportAt = &at
	}
	var rows []model.ScalingExecution
	if err := s.DB.WithContext(ctx).Select("result", "degrade_state", "created_at", "reported_at").Where("created_at >= ?", since).
		Order("id DESC").Limit(maxReportRows).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		out.ReportsInWindow++
		out.ByResult[r.Result]++
		state := r.DegradeState
		if state == "" {
			state = "unknown"
		}
		out.ByState[state]++
		if r.ReportedAt != nil {
			if late := r.ReportedAt.Sub(r.CreatedAt); late > lateAfter {
				out.Late++
				if sec := int64(late.Seconds()); sec > out.MaxLateSeconds {
					out.MaxLateSeconds = sec
				}
			}
		}
	}
	return nil
}

func (s *Service) jobStatus(ctx context.Context, since time.Time, out *JobStatus) error {
	out.ByStatus, out.RecentFailures = map[string]int64{}, []JobFailure{}
	var counts []struct {
		Status string
		N      int64
	}
	if err := s.DB.WithContext(ctx).Model(&model.Job{}).Select("status, COUNT(*) AS n").Where("created_at >= ?", since).Group("status").Scan(&counts).Error; err != nil {
		return err
	}
	for _, c := range counts {
		out.ByStatus[c.Status] = c.N
	}
	var failed []model.Job
	if err := s.DB.WithContext(ctx).Where("status = ? AND created_at >= ?", "failed", since).Order("id DESC").Limit(maxFailures).Find(&failed).Error; err != nil {
		return err
	}
	for _, j := range failed {
		out.RecentFailures = append(out.RecentFailures, JobFailure{ID: j.ID, Type: j.JobType, Target: j.TargetName, Error: clip(j.Result, 300), FinishAt: j.FinishedAt})
	}
	return nil
}

// labels are the words of the Markdown export, in the two languages of the console.
var labels = map[string]map[string]string{
	"zh": {
		"title": "服务运行状态", "generated": "生成时间", "window": "统计范围（小时）", "service": "平台", "version": "版本", "started": "启动时间", "uptime": "已运行（秒）",
		"database": "数据库", "switches": "开关", "on": "开", "off": "关", "modules": "模块", "total": "全部", "enabled": "已启用", "auto": "自动", "admission": "准入",
		"recommend": "仅推荐", "observe": "仅观察", "predicting": "做预测", "bypass": "旁路", "attention": "需要关注", "activity": "扩缩容与预测",
		"ups": "扩容次数", "downs": "缩容次数", "failed": "失败的预测运行", "operator": "operator", "seen": "最近一次读取模块或设置", "report": "最近一次上报",
		"reports": "窗口内上报", "late": "补报（动作后超过一分钟才到平台）", "maxlate": "最长延迟（秒）", "never": "没有", "jobs": "后台任务", "failures": "最近失败的任务",
		"none": "无", "name": "模块", "mode": "模式", "flags": "异常", "lastError": "最近失败", "workload": "工作负载", "failuresIn": "连续失败",
	},
	"en": {
		"title": "Service status", "generated": "Generated at", "window": "Window (hours)", "service": "Platform", "version": "Version", "started": "Started at", "uptime": "Uptime (seconds)",
		"database": "Database", "switches": "Switches", "on": "on", "off": "off", "modules": "Modules", "total": "Total", "enabled": "Enabled", "auto": "Auto", "admission": "Admission",
		"recommend": "Recommend only", "observe": "Observe only", "predicting": "Forecasting", "bypass": "Bypass", "attention": "Need a look", "activity": "Scaling and forecasts",
		"ups": "Scale-ups", "downs": "Scale-downs", "failed": "Failed forecast runs", "operator": "Operator", "seen": "Last read of modules or settings", "report": "Last report",
		"reports": "Reports in the window", "late": "Late reports (reached the platform over a minute after the action)", "maxlate": "Longest delay (seconds)", "never": "none", "jobs": "Background tasks", "failures": "Recent failed tasks",
		"none": "none", "name": "Module", "mode": "Mode", "flags": "Problems", "lastError": "Last failure", "workload": "Workload", "failuresIn": "failures in a row",
	},
}

func sortedCounts(m map[string]int64) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// Markdown renders the snapshot as a document a person can read, keep and attach to a ticket. lang is "zh" or "en" (zh when unknown).
func (r *StatusReport) Markdown(lang string) string {
	l := labels[lang]
	if l == nil {
		l = labels["zh"]
	}
	at := func(t *time.Time) string {
		if t == nil {
			return l["never"]
		}
		return t.UTC().Format(time.RFC3339)
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# %s", l["title"])
	w("")
	w("- %s: %s (UTC)", l["generated"], r.GeneratedAt.UTC().Format(time.RFC3339))
	w("- %s: %d", l["window"], r.Hours)
	w("")
	w("## %s", l["service"])
	w("- %s: %s", l["version"], r.Service.Version)
	w("- %s: %s", l["started"], r.Service.StartedAt.UTC().Format(time.RFC3339))
	w("- %s: %d", l["uptime"], r.Service.UptimeSeconds)
	w("- %s: %s", l["database"], r.Service.Database)
	keys := make([]string, 0, len(r.Switches))
	for k := range r.Switches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		state := l["off"]
		if r.Switches[k] {
			state = l["on"]
		}
		w("- %s: %s", k, state)
	}
	m := r.Modules
	w("")
	w("## %s", l["modules"])
	w("- %s: %d, %s: %d, %s: %d, %s: %d, %s: %d, %s: %d", l["total"], m.Total, l["enabled"], m.Enabled, l["auto"], m.Auto, l["admission"], m.Admission, l["recommend"], m.Recommend, l["observe"], m.Observe)
	w("- %s: %d, %s: %d, %s: %d", l["predicting"], m.Predicting, l["bypass"], m.Bypass, l["attention"], m.Attention)
	if len(r.Attention) > 0 {
		w("")
		w("| %s | %s | %s | %s | %s |", l["name"], l["workload"], l["mode"], l["flags"], l["lastError"])
		w("|---|---|---|---|---|")
		for _, a := range r.Attention {
			failure := "-"
			if a.LastError != "" {
				failure = clip(strings.ReplaceAll(a.LastError, "|", "/"), 120)
				if a.ConsecutiveFailures > 0 {
					failure += fmt.Sprintf(" (%d %s)", a.ConsecutiveFailures, l["failuresIn"])
				}
			}
			w("| %s | %s/%s | %s | %s | %s |", a.Name, a.Namespace, a.Workload, a.Mode, strings.Join(a.Flags, ", "), failure)
		}
	}
	w("")
	w("## %s", l["activity"])
	w("- %s: %d, %s: %d, %s: %d", l["ups"], r.Activity.ScaleUps, l["downs"], r.Activity.ScaleDowns, l["failed"], r.Activity.FailedRuns)
	o := r.Operator
	w("")
	w("## %s", l["operator"])
	w("- %s: %s", l["seen"], at(o.LastSeenAt))
	w("- %s: %s", l["report"], at(o.LastReportAt))
	w("- %s: %d (%s)", l["reports"], o.ReportsInWindow, sortedCounts(o.ByResult))
	w("- state: %s", sortedCounts(o.ByState))
	w("- %s: %d, %s: %d", l["late"], o.Late, l["maxlate"], o.MaxLateSeconds)
	w("")
	w("## %s", l["jobs"])
	w("- %s", sortedCounts(r.Jobs.ByStatus))
	if len(r.Jobs.RecentFailures) > 0 {
		w("")
		w("%s:", l["failures"])
		for _, f := range r.Jobs.RecentFailures {
			w("- #%d %s %s: %s", f.ID, f.Type, f.Target, f.Error)
		}
	}
	return b.String()
}
