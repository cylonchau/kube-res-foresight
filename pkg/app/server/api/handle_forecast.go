package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/decision"
	"github.com/cylonchau/kubeforecast/pkg/forecast"
	"github.com/cylonchau/kubeforecast/pkg/forecastsvc"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
)

// pipelineTimeout bounds one manually triggered pipeline step.
const pipelineTimeout = 5 * time.Minute

// ForecastRouter serves modules (Kubernetes workloads) with their metric sources and fixed periods, the platform-wide
// algorithm settings and the forecasting pipeline.
type ForecastRouter struct {
	Modules     *rdb.ModuleDAO
	Configs     *rdb.AlgorithmDAO
	Mappings    *rdb.MetricMappingDAO
	Predictions *rdb.PredictionDAO
	Service     *forecastsvc.Service
	Config      *rdb.SysConfigDAO
}

// Configuration keys that open the modes writing to the cluster. They default to false: Admission and Auto
// are enabled by an administrator only after the Kyverno policy and the operator have been verified in the
// target cluster.
const (
	ConfigAllowAdmissionMode = "forecast.allow_admission_mode"
	ConfigAllowAutoMode      = "forecast.allow_auto_mode"
)

// modeAllowed reports whether a module may be put in the mode.
func (r *ForecastRouter) modeAllowed(ctx context.Context, mode string) error {
	key := ""
	switch mode {
	case "", model.ModeObserve, model.ModeRecommend:
		return nil
	case model.ModeAdmission:
		key = ConfigAllowAdmissionMode
	case model.ModeAuto:
		key = ConfigAllowAutoMode
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}
	if r.Config != nil {
		if allowed, err := r.Config.GetBool(ctx, key, false); err == nil && allowed {
			return nil
		}
	}
	return fmt.Errorf("mode %q is disabled: an administrator must set %s=true after verifying it in the target cluster", mode, key)
}

func (r *ForecastRouter) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, rdb.ErrNameTaken), errors.Is(err, rdb.ErrModuleEnabled), errors.Is(err, forecastsvc.ErrNotConfigured), errors.Is(err, rdb.ErrWorkloadTaken):
		query.BadRequest(c, err)
	case errors.Is(err, forecastsvc.ErrObserveOnly), errors.Is(err, forecastsvc.ErrStale):
		query.ErrorResponse(c, http.StatusConflict, err)
	default:
		query.HandleDBError(c, err)
	}
}

func (r *ForecastRouter) module(c *gin.Context) (*model.ForecastModule, bool) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return nil, false
	}
	m, err := r.Modules.Get(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return nil, false
	}
	return m, true
}

func (r *ForecastRouter) ListModules(c *gin.Context) {
	var req apis.ModuleListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	list, total, err := r.Modules.List(c.Request.Context(), rdb.ModuleFilter{Keyword: req.Keyword, Namespace: req.Namespace, Mode: req.Mode, Enabled: boolQuery(c, "enabled")}, int(req.Page), int(req.PageSize))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	summaries, err := r.Predictions.ModuleSummaries(c.Request.Context(), list)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]moduleDTO, len(list))
	for i, m := range list {
		out[i] = moduleDTO{ForecastModule: m, Summary: summaries[m.ID]}
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": req.Page, "pageSize": req.PageSize})
}

// moduleDTO is a module with what it is connected to.
type moduleDTO struct {
	*model.ForecastModule
	Summary rdb.ModuleSummary `json:"summary"`
}

func (r *ForecastRouter) GetModule(c *gin.Context) {
	if m, ok := r.module(c); ok {
		query.SuccessResponse(c, nil, m)
	}
}

func (r *ForecastRouter) CreateModule(c *gin.Context) {
	var req apis.ModuleCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.modeAllowed(c.Request.Context(), req.Mode); err != nil {
		query.BadRequest(c, err)
		return
	}
	m := &model.ForecastModule{
		Name: req.Name, Description: req.Description, Enabled: req.Enabled == nil || *req.Enabled, Mode: req.Mode,
		Namespace: strings.TrimSpace(req.Namespace), WorkloadKind: req.WorkloadKind, Workload: strings.TrimSpace(req.Workload), Service: req.Service, Container: req.Container,
		LookbackHours: req.LookbackHours, HorizonMinutes: req.HorizonMinutes, MaxFillSteps: req.MaxFillSteps,
		StepSeconds: req.StepSeconds, MinPoints: req.MinPoints, WindowSteps: req.WindowSteps, RunIntervalMinutes: req.RunIntervalMinutes,
		LimitCPURatio: req.LimitCPURatio, LimitMemoryRatio: req.LimitMemoryRatio,
		ScaleUpMaxStep: req.ScaleUpMaxStep, ScaleDownMaxStep: req.ScaleDownMaxStep,
		ScaleUpCooldownSeconds: req.ScaleUpCooldownSeconds, ScaleDownCooldownSeconds: req.ScaleDownCooldownSeconds,
		FallbackMinHistoryRatio: req.FallbackMinHistoryRatio, MaxMissingRatio: req.MaxMissingRatio,
		TTLSeconds: req.TTLSeconds, MinScore: req.MinScore,
		MinCPUMilli: req.MinCPUMilli, MaxCPUMilli: req.MaxCPUMilli, MinMemoryBytes: req.MinMemoryBytes, MaxMemoryBytes: req.MaxMemoryBytes,
		MinReplicas: req.MinReplicas, MaxReplicas: req.MaxReplicas,
		FallbackCPUMilli: req.FallbackCPUMilli, FallbackMemoryBytes: req.FallbackMemoryBytes, FallbackReplicas: req.FallbackReplicas,
		MinGPU: req.MinGPU, MaxGPU: req.MaxGPU, FallbackGPU: req.FallbackGPU,
		HeadroomRatio: req.HeadroomRatio, PodCapacityRPS: req.PodCapacityRPS, Timezone: req.Timezone,
		ReplicaMode: req.ReplicaMode, CapacityMode: req.CapacityMode,
		PodCPURequestMilli: req.PodCPURequestMilli, PodMemoryRequestBytes: req.PodMemoryRequestBytes, TargetUtilization: req.TargetUtilization,
		MaxReplicasMode: req.MaxReplicasMode, HardMaxReplicas: req.HardMaxReplicas, ExpandStepRatio: req.ExpandStepRatio,
		OOMLookbackDays: req.OOMLookbackDays, OOMMemoryUplift: req.OOMMemoryUplift, JVMHeapPressure: 0.85, RecoverCycles: req.RecoverCycles,
	}
	if req.JVMHeapPressure != nil {
		m.JVMHeapPressure = *req.JVMHeapPressure
	}
	if err := m.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	// the sources are checked before anything is stored
	ctx := c.Request.Context()
	sources := make([]*model.MetricSource, 0, len(req.Sources))
	var made []uint // mappings written for custom queries, removed again when the module is not created
	for i := range req.Sources {
		src, mapping, err := r.sourceFromReq(ctx, 0, &req.Sources[i])
		if mapping != 0 {
			made = append(made, mapping)
		}
		if err == nil {
			err = r.checkDataset(ctx, m, src)
		}
		if err != nil {
			r.dropMappings(ctx, made)
			query.BadRequest(c, fmt.Errorf("source %q: %w", req.Sources[i].Name, err))
			return
		}
		sources = append(sources, src)
	}
	if err := r.Modules.Create(ctx, m); err != nil {
		r.dropMappings(ctx, made)
		r.writeError(c, err)
		return
	}
	for _, src := range sources {
		src.ModuleID = m.ID
		if err := r.Modules.SaveSource(ctx, src); err != nil {
			_ = r.Modules.Delete(ctx, m.ID)
			r.dropMappings(ctx, made)
			r.writeError(c, err)
			return
		}
	}
	created, err := r.Modules.Get(ctx, m.ID)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, created)
}

func applyModuleUpdate(m *model.ForecastModule, req *apis.ModuleUpdateReq) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&m.Name, req.Name)
	set(&m.Namespace, req.Namespace)
	set(&m.WorkloadKind, req.WorkloadKind)
	set(&m.Workload, req.Workload)
	set(&m.Service, req.Service)
	set(&m.Container, req.Container)
	set(&m.Timezone, req.Timezone)
	set(&m.ReplicaMode, req.ReplicaMode)
	set(&m.CapacityMode, req.CapacityMode)
	set(&m.MaxReplicasMode, req.MaxReplicasMode)
	if req.Description != nil {
		m.Description = *req.Description
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if req.Mode != nil {
		m.Mode = *req.Mode
	}
	for dst, src := range map[*int]*int{
		&m.LookbackHours: req.LookbackHours, &m.HorizonMinutes: req.HorizonMinutes, &m.MaxFillSteps: req.MaxFillSteps,
		&m.StepSeconds: req.StepSeconds, &m.MinPoints: req.MinPoints, &m.WindowSteps: req.WindowSteps, &m.RunIntervalMinutes: req.RunIntervalMinutes,
		&m.TTLSeconds: req.TTLSeconds, &m.MinReplicas: req.MinReplicas, &m.MaxReplicas: req.MaxReplicas, &m.FallbackReplicas: req.FallbackReplicas,
		&m.HardMaxReplicas: req.HardMaxReplicas, &m.MinGPU: req.MinGPU, &m.MaxGPU: req.MaxGPU, &m.FallbackGPU: req.FallbackGPU,
		&m.OOMLookbackDays: req.OOMLookbackDays, &m.RecoverCycles: req.RecoverCycles,
		&m.ScaleUpMaxStep: req.ScaleUpMaxStep, &m.ScaleDownMaxStep: req.ScaleDownMaxStep,
		&m.ScaleUpCooldownSeconds: req.ScaleUpCooldownSeconds, &m.ScaleDownCooldownSeconds: req.ScaleDownCooldownSeconds,
	} {
		if src != nil {
			*dst = *src
		}
	}
	for dst, src := range map[*int64]*int64{
		&m.MinCPUMilli: req.MinCPUMilli, &m.MaxCPUMilli: req.MaxCPUMilli, &m.MinMemoryBytes: req.MinMemoryBytes, &m.MaxMemoryBytes: req.MaxMemoryBytes,
		&m.FallbackCPUMilli: req.FallbackCPUMilli, &m.FallbackMemoryBytes: req.FallbackMemoryBytes,
		&m.PodCPURequestMilli: req.PodCPURequestMilli, &m.PodMemoryRequestBytes: req.PodMemoryRequestBytes,
	} {
		if src != nil {
			*dst = *src
		}
	}
	for dst, src := range map[*float64]*float64{
		&m.LimitCPURatio: req.LimitCPURatio, &m.LimitMemoryRatio: req.LimitMemoryRatio, &m.HeadroomRatio: req.HeadroomRatio, &m.PodCapacityRPS: req.PodCapacityRPS, &m.TargetUtilization: req.TargetUtilization,
		&m.FallbackMinHistoryRatio: req.FallbackMinHistoryRatio, &m.MaxMissingRatio: req.MaxMissingRatio,
		&m.OOMMemoryUplift: req.OOMMemoryUplift, &m.JVMHeapPressure: req.JVMHeapPressure, &m.MinScore: req.MinScore, &m.ExpandStepRatio: req.ExpandStepRatio,
	} {
		if src != nil {
			*dst = *src
		}
	}
}

func (r *ForecastRouter) UpdateModule(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	var req apis.ModuleUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if req.Mode != nil {
		if err := r.modeAllowed(c.Request.Context(), *req.Mode); err != nil {
			query.BadRequest(c, err)
			return
		}
	}
	applyModuleUpdate(m, &req)
	if err := m.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	for _, src := range m.Sources {
		if err := r.checkDataset(c.Request.Context(), m, src); err != nil {
			query.BadRequest(c, fmt.Errorf("source %q: %w", src.Name, err))
			return
		}
	}
	if err := r.Modules.Update(c.Request.Context(), m); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, m)
}

func (r *ForecastRouter) DeleteModule(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.Modules.Delete(c.Request.Context(), id); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *ForecastRouter) AlgorithmSchema(c *gin.Context) {
	if err := r.Service.EnsureAlgorithms(c.Request.Context()); err != nil {
		query.InternalError(c, err)
		return
	}
	def, ok := forecast.Lookup(c.Param("id"))
	if !ok {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	query.SuccessResponse(c, nil, def)
}

func (r *ForecastRouter) ListPeriods(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	list, err := r.Modules.Periods(c.Request.Context(), m.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, list)
}

func periodFromReq(moduleID uint, req *apis.PeriodReq) (*model.ModulePeriod, error) {
	p := &model.ModulePeriod{
		ModuleID: moduleID, Name: strings.TrimSpace(req.Name), Enabled: req.Enabled == nil || *req.Enabled, Priority: req.Priority,
		Weekdays: req.Weekdays, StartMinute: req.StartMinute, EndMinute: req.EndMinute, AbsStart: req.AbsStart, AbsEnd: req.AbsEnd,
		CPUMilli: req.CPUMilli, MemoryBytes: req.MemoryBytes, Replicas: req.Replicas, Remark: req.Remark,
	}
	if p.Weekdays < 0 || p.Weekdays > 127 {
		return nil, fmt.Errorf("weekdays must be a bit mask between 0 and 127")
	}
	err := decision.ValidatePeriod(decision.FixedPeriod{
		Name: p.Name, Weekdays: uint8(p.Weekdays), StartMinute: p.StartMinute, EndMinute: p.EndMinute, AbsStart: p.AbsStart, AbsEnd: p.AbsEnd,
		CPUMilli: p.CPUMilli, MemoryBytes: p.MemoryBytes, Replicas: p.Replicas,
	})
	return p, err
}

func (r *ForecastRouter) CreatePeriod(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	var req apis.PeriodReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p, err := periodFromReq(m.ID, &req)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Modules.SavePeriod(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

func (r *ForecastRouter) UpdatePeriod(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	pid, ok := parseUintParam(c, "pid")
	if !ok {
		return
	}
	existing, err := r.Modules.GetPeriod(c.Request.Context(), pid)
	if err != nil || existing.ModuleID != m.ID {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	var req apis.PeriodReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p, err := periodFromReq(m.ID, &req)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	p.ID, p.CreatedAt = existing.ID, existing.CreatedAt
	if err := r.Modules.SavePeriod(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

func (r *ForecastRouter) DeletePeriod(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	pid, ok := parseUintParam(c, "pid")
	if !ok {
		return
	}
	if err := r.Modules.DeletePeriod(c.Request.Context(), id, pid); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Recommend decides and stores a new recommendation version for a module outside observe mode.
func (r *ForecastRouter) Recommend(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	rec, detail, err := r.Service.Recommend(c.Request.Context(), id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"recommendation": rec, "detail": detail})
}

func recommendationFilter(c *gin.Context) rdb.RecommendationFilter {
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	version, _ := strconv.Atoi(c.Query("version"))
	return rdb.RecommendationFilter{ModuleID: uint(moduleID), Mode: c.Query("mode"), Version: version, Source: c.Query("source"),
		Degraded: boolQuery(c, "degraded"), OOMFloor: boolQuery(c, "oom_floor"),
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
}

func (r *ForecastRouter) ListRecommendations(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.SearchRecommendations(c.Request.Context(), recommendationFilter(c), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// FallbackRecord is a snapshot where a field did not come from a usable forecast, with how long that lasted.
type FallbackRecord struct {
	*model.Recommendation
	Reasons map[string]string `json:"reasons"`
	Notes   map[string]string `json:"notes"`
	// RecoveredAt is the time of the first later healthy snapshot; nil while the module is still degraded.
	RecoveredAt     *time.Time `json:"recovered_at"`
	DurationSeconds int64      `json:"duration_seconds"`
	Ongoing         bool       `json:"ongoing"`
}

// fallbackReasons are the reason codes the filter accepts.
var fallbackReasons = map[string]bool{
	forecastsvc.DegradeNoData: true, forecastsvc.DegradeStaleData: true, forecastsvc.DegradeInsufficient: true,
	forecastsvc.DegradePredictorFailed: true, forecastsvc.DegradeNoPrediction: true, forecastsvc.DegradeStalePrediction: true,
	forecastsvc.DegradeNotEligible: true, forecastsvc.DegradeRecovering: true, "oom_floor": true,
}

func (r *ForecastRouter) fallbackRecord(ctx context.Context, rec *model.Recommendation, now time.Time) (FallbackRecord, error) {
	out := FallbackRecord{Recommendation: rec, Reasons: map[string]string{}, Notes: map[string]string{}}
	if rec.DegradeReasons != "" {
		_ = json.Unmarshal([]byte(rec.DegradeReasons), &out.Reasons)
	}
	if rec.OOMFloor {
		out.Reasons["memory_floor"] = "oom_floor"
	}
	var detail forecastsvc.RecommendDetail
	if json.Unmarshal([]byte(rec.Detail), &detail) == nil {
		for field, note := range detail.DegradeNotes {
			out.Notes[field] = note
		}
		if rec.OOMFloor && detail.OOM != nil {
			out.Notes["memory_floor"] = detail.OOM.Reason
		}
	}
	next, err := r.Predictions.NextHealthy(ctx, rec.ModuleID, rec.Version)
	if err != nil {
		return out, err
	}
	end := now
	if next != nil {
		out.RecoveredAt, end = &next.CreatedAt, next.CreatedAt
	} else {
		out.Ongoing = true
	}
	if d := end.Sub(rec.CreatedAt); d > 0 {
		out.DurationSeconds = int64(d / time.Second)
	}
	return out, nil
}

// ListFallbacks serves the degradation records: every snapshot where a field fell back, kept its original value
// or was raised by the OOM protection.
func (r *ForecastRouter) ListFallbacks(c *gin.Context) {
	page, size := pageParams(c)
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	reason := c.Query("reason")
	if reason != "" && !fallbackReasons[reason] {
		query.BadRequest(c, fmt.Errorf("unknown reason %q", reason))
		return
	}
	list, total, err := r.Predictions.SearchFallbacks(c.Request.Context(), rdb.FallbackFilter{ModuleID: uint(moduleID), Reason: reason,
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	now := time.Now()
	out := make([]FallbackRecord, 0, len(list))
	for _, rec := range list {
		item, err := r.fallbackRecord(c.Request.Context(), rec, now)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		out = append(out, item)
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// Bypass makes a module keep every original value. The reason is mandatory and, like every write, is audited
// by the operation-log middleware.
func (r *ForecastRouter) Bypass(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req apis.BypassReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		query.BadRequest(c, errors.New("a reason is required to bypass a module"))
		return
	}
	if err := r.Modules.SetBypass(c.Request.Context(), id, true, strings.TrimSpace(req.Reason)); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func (r *ForecastRouter) Resume(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.Modules.SetBypass(c.Request.Context(), id, false, ""); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

func jsonMarshal(v any) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

// Leaderboard ranks the algorithm instances bound to a module on the sources of one kind by observed accuracy.
func (r *ForecastRouter) Leaderboard(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	kind := c.Query("kind")
	if _, known := model.MetricKinds[kind]; !known {
		query.BadRequest(c, fmt.Errorf("kind must be a metric kind such as cpu or memory"))
		return
	}
	entries, err := r.Service.Leaderboard(c.Request.Context(), m.ID, kind, sinceHours(c))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, entries)
}

func marshalJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
